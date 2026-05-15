package database

import "time"

// DistributedLock 进程间互斥锁（基于 DB 行级锁 + 心跳）。
type DistributedLock struct {
	ID          uint      `gorm:"primaryKey"`
	LockKey     string    `gorm:"<-:create;uniqueIndex;not null;size:128"`
	OwnerID     string    `gorm:"not null;size:64"`
	AcquiredAt  time.Time `gorm:"not null;index"`
	HeartbeatAt time.Time `gorm:"not null"`
	ExpiresAt   time.Time `gorm:"not null;index"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
}
