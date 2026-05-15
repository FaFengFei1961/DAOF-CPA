package database

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gorm.io/gorm/clause"
)

var distributedLockOwnerID = newDistributedLockOwnerID()

// AcquireLock tries to acquire a process-wide distributed lock.
//
// The returned ownerID is the current process owner token. If acquired is false,
// another live owner currently holds the lock.
func AcquireLock(key string, ttl time.Duration) (ownerID string, acquired bool, err error) {
	return acquireLockForOwner(key, distributedLockOwnerID, ttl)
}

func acquireLockForOwner(key, ownerID string, ttl time.Duration) (string, bool, error) {
	if err := validateDistributedLockInput(key, ownerID, ttl); err != nil {
		return ownerID, false, err
	}
	if DB == nil {
		return ownerID, false, errors.New("database is not initialized")
	}

	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		acquired, err := tryAcquireLockForOwner(key, ownerID, ttl)
		if err == nil {
			return ownerID, acquired, nil
		}
		if !isTransientLockError(err) {
			return ownerID, false, err
		}
		lastErr = err
		time.Sleep(time.Duration(attempt+1) * 10 * time.Millisecond)
	}
	return ownerID, false, lastErr
}

func tryAcquireLockForOwner(key, ownerID string, ttl time.Duration) (bool, error) {
	now := time.Now().UTC()
	expiresAt := now.Add(ttl)
	updates := map[string]any{
		"owner_id":     ownerID,
		"acquired_at":  now,
		"heartbeat_at": now,
		"expires_at":   expiresAt,
	}

	res := DB.Model(&DistributedLock{}).
		Where("lock_key = ? AND expires_at <= ?", key, now).
		Updates(updates)
	if res.Error != nil {
		return false, res.Error
	}
	if res.RowsAffected > 0 {
		return true, nil
	}

	lock := DistributedLock{
		LockKey:     key,
		OwnerID:     ownerID,
		AcquiredAt:  now,
		HeartbeatAt: now,
		ExpiresAt:   expiresAt,
	}
	res = DB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "lock_key"}},
		DoNothing: true,
	}).Create(&lock)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// RenewLock extends the heartbeat and expiry for the current lock owner.
func RenewLock(key, ownerID string, ttl time.Duration) (renewed bool, err error) {
	if err := validateDistributedLockInput(key, ownerID, ttl); err != nil {
		return false, err
	}
	if DB == nil {
		return false, errors.New("database is not initialized")
	}

	now := time.Now().UTC()
	res := DB.Model(&DistributedLock{}).
		Where("lock_key = ? AND owner_id = ? AND expires_at > ?", key, ownerID, now).
		Updates(map[string]any{
			"heartbeat_at": now,
			"expires_at":   now.Add(ttl),
		})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// ReleaseLock releases a lock only if the caller still owns it.
func ReleaseLock(key, ownerID string) error {
	if key = strings.TrimSpace(key); key == "" {
		return errors.New("lock key is required")
	}
	if ownerID = strings.TrimSpace(ownerID); ownerID == "" {
		return errors.New("lock owner is required")
	}
	if DB == nil {
		return errors.New("database is not initialized")
	}
	return DB.Where("lock_key = ? AND owner_id = ?", key, ownerID).
		Delete(&DistributedLock{}).Error
}

func validateDistributedLockInput(key, ownerID string, ttl time.Duration) error {
	if strings.TrimSpace(key) == "" {
		return errors.New("lock key is required")
	}
	if len(key) > 128 {
		return errors.New("lock key exceeds 128 bytes")
	}
	if strings.TrimSpace(ownerID) == "" {
		return errors.New("lock owner is required")
	}
	if len(ownerID) > 64 {
		return errors.New("lock owner exceeds 64 bytes")
	}
	if ttl <= 0 {
		return errors.New("lock ttl must be positive")
	}
	return nil
}

func newDistributedLockOwnerID() string {
	machineID := readMachineID()
	if machineID == "" {
		machineID = "unknown-machine"
	}
	machineHash := sha256.Sum256([]byte(machineID))
	return fmt.Sprintf("%s-%d-%d",
		hex.EncodeToString(machineHash[:12]),
		os.Getpid(),
		time.Now().UnixNano(),
	)
}

func readMachineID() string {
	for _, value := range []string{
		os.Getenv("DAOF_MACHINE_UUID"),
		os.Getenv("MACHINE_UUID"),
		os.Getenv("COMPUTERNAME"),
	} {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}

	for _, path := range []string{
		"/etc/machine-id",
		"/var/lib/dbus/machine-id",
		filepath.Join(os.Getenv("ProgramData"), "Microsoft", "Crypto", "RSA", "MachineKeys"),
	} {
		if path == "" {
			continue
		}
		if data, err := os.ReadFile(path); err == nil {
			if value := strings.TrimSpace(string(data)); value != "" {
				return value
			}
		}
	}

	if hostname, err := os.Hostname(); err == nil {
		return strings.TrimSpace(hostname)
	}
	return ""
}

func isTransientLockError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "database is locked") ||
		strings.Contains(msg, "database table is locked") ||
		strings.Contains(msg, "deadlock detected") ||
		strings.Contains(msg, "lock wait timeout") ||
		strings.Contains(msg, "could not serialize access") ||
		strings.Contains(msg, "sqlstate 40001") ||
		strings.Contains(msg, "sqlstate 40p01") ||
		strings.Contains(msg, "sqlstate 55p03")
}
