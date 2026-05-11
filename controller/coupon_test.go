package controller

import (
	"testing"
	"time"

	"daof-ai-hub/database"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupCouponTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(
		&database.Package{}, &database.UserSubscription{},
		&database.CouponTemplate{}, &database.UserCoupon{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	prev := database.DB
	database.DB = db
	t.Cleanup(func() { database.DB = prev })
	return db
}

// ─── 模板校验 ─────────────────────────────────────────────────────────

func TestValidateTemplate_Required(t *testing.T) {
	cases := []struct {
		name    string
		tpl     database.CouponTemplate
		wantErr bool
	}{
		{"空名拒", database.CouponTemplate{Name: ""}, true},
		{"合法", database.CouponTemplate{Name: "x", DiscountType: "fixed_price", DiscountValue: 10, ValidDays: 30}, false},
		{"负值拒", database.CouponTemplate{Name: "x", DiscountType: "fixed_price", DiscountValue: -1}, true},
		{"未知类型拒", database.CouponTemplate{Name: "x", DiscountType: "weird", DiscountValue: 10}, true},
		{"负有效期拒", database.CouponTemplate{Name: "x", DiscountType: "fixed_price", ValidDays: -1}, true},
		{"非法 package_ids JSON 拒", database.CouponTemplate{Name: "x", DiscountType: "fixed_price", PackageIDs: "not-json"}, true},
		{"合法 package_ids JSON 通过", database.CouponTemplate{Name: "x", DiscountType: "fixed_price", PackageIDs: "[1,2,3]"}, false},
		{"空 package_ids 通过", database.CouponTemplate{Name: "x", DiscountType: "fixed_price", PackageIDs: ""}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateTemplate(&tc.tpl)
			gotErr := err != nil
			if gotErr != tc.wantErr {
				t.Errorf("got err=%v want err=%v", err, tc.wantErr)
			}
		})
	}
}

// ─── EffectivePrice / Apply ────────────────────────────────────────────

func TestSnapshotEffectivePrice(t *testing.T) {
	uc := &database.UserCoupon{SnapshotType: "fixed_price", SnapshotValue: 10}
	if got := uc.SnapshotEffectivePrice(20); got != 10 {
		t.Errorf("expected 10 got %v", got)
	}
	// 防御：值大于原价时退化为原价
	uc.SnapshotValue = 30
	if got := uc.SnapshotEffectivePrice(20); got != 20 {
		t.Errorf("expected 20 got %v (when snapshot > base)", got)
	}
	// 负值（admin 直改 DB） → 0
	uc.SnapshotValue = -5
	if got := uc.SnapshotEffectivePrice(20); got != 0 {
		t.Errorf("expected 0 got %v (negative snapshot)", got)
	}
	// 未知类型 → 退回原价
	uc.SnapshotType = "unknown"
	uc.SnapshotValue = 5
	if got := uc.SnapshotEffectivePrice(20); got != 20 {
		t.Errorf("expected 20 got %v (unknown type)", got)
	}
}

func TestIsAvailable_StatusGate(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		uc   database.UserCoupon
		want bool
	}{
		{"available 无过期", database.UserCoupon{Status: "available"}, true},
		{"used 不可用", database.UserCoupon{Status: "used"}, false},
		{"revoked 不可用", database.UserCoupon{Status: "revoked"}, false},
		{"expired 不可用", database.UserCoupon{Status: "expired"}, false},
		{"available 已过期", database.UserCoupon{
			Status:    "available",
			ExpiresAt: ptrTime(now.Add(-1 * time.Hour)),
		}, false},
		{"available 未过期", database.UserCoupon{
			Status:    "available",
			ExpiresAt: ptrTime(now.Add(24 * time.Hour)),
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.uc.IsAvailable(now); got != tc.want {
				t.Errorf("got=%v want=%v", got, tc.want)
			}
		})
	}
}

func TestAppliesToPackage(t *testing.T) {
	uc := &database.UserCoupon{}
	// 空 allowed = 全适用
	if !uc.AppliesToPackage(99, nil) {
		t.Error("nil allowed should apply to any package")
	}
	if !uc.AppliesToPackage(99, []uint{}) {
		t.Error("empty allowed should apply to any package")
	}
	// 限定列表
	if uc.AppliesToPackage(99, []uint{1, 2, 3}) {
		t.Error("99 not in [1,2,3] should not apply")
	}
	if !uc.AppliesToPackage(2, []uint{1, 2, 3}) {
		t.Error("2 in [1,2,3] should apply")
	}
}

// ─── lockAndApplyCoupon 事务路径 ────────────────────────────────────────

func TestLockAndApplyCoupon_HappyPath(t *testing.T) {
	db := setupCouponTestDB(t)
	uc := database.UserCoupon{
		UserID: 1, TemplateID: 1, Code: "CP-1-1-aaa",
		Status: "available", SnapshotType: "fixed_price", SnapshotValue: 10, SnapshotPackageIDs: "",
	}
	if err := db.Create(&uc).Error; err != nil {
		t.Fatalf("create: %v", err)
	}
	pkg := &database.Package{}
	pkg.ID = 5
	pkg.PriceAmount = 20

	var got *database.UserCoupon
	err := db.Transaction(func(tx *gorm.DB) error {
		c, err := lockAndApplyCoupon(tx, 1, uc.ID, pkg)
		got = c
		return err
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got.Status != "used" {
		t.Errorf("expected status=used got %s", got.Status)
	}
	if got.UsedAt == nil {
		t.Error("expected UsedAt to be set")
	}
}

func TestLockAndApplyCoupon_AlreadyUsed(t *testing.T) {
	db := setupCouponTestDB(t)
	uc := database.UserCoupon{
		UserID: 1, Code: "CP-1-1-bbb",
		Status: "used", SnapshotType: "fixed_price", SnapshotValue: 10,
	}
	db.Create(&uc)
	pkg := &database.Package{}
	pkg.ID = 5
	pkg.PriceAmount = 20

	err := db.Transaction(func(tx *gorm.DB) error {
		_, err := lockAndApplyCoupon(tx, 1, uc.ID, pkg)
		return err
	})
	if err == nil {
		t.Error("expected error for already-used coupon")
	}
}

func TestLockAndApplyCoupon_WrongUser(t *testing.T) {
	db := setupCouponTestDB(t)
	uc := database.UserCoupon{
		UserID: 1, Code: "CP-1-1-ccc",
		Status: "available", SnapshotType: "fixed_price", SnapshotValue: 10,
	}
	db.Create(&uc)
	pkg := &database.Package{}
	pkg.ID = 5
	pkg.PriceAmount = 20

	// userID=2 试图用 user 1 的券
	err := db.Transaction(func(tx *gorm.DB) error {
		_, err := lockAndApplyCoupon(tx, 2, uc.ID, pkg)
		return err
	})
	if err == nil {
		t.Error("expected error for cross-user coupon use")
	}
}

func TestLockAndApplyCoupon_PackageNotApplicable(t *testing.T) {
	db := setupCouponTestDB(t)
	uc := database.UserCoupon{
		UserID: 1, Code: "CP-1-1-ddd",
		Status: "available", SnapshotType: "fixed_price", SnapshotValue: 10,
		SnapshotPackageIDs: "[1,2]",
	}
	db.Create(&uc)
	pkg := &database.Package{}
	pkg.ID = 99
	pkg.PriceAmount = 20

	err := db.Transaction(func(tx *gorm.DB) error {
		_, err := lockAndApplyCoupon(tx, 1, uc.ID, pkg)
		return err
	})
	if err == nil {
		t.Error("expected error when package not in allowed list")
	}
}

func ptrTime(t time.Time) *time.Time { return &t }

// ─── R23+2 第二轮交叉审查后的修复测试 ─────────────────────────────────────

// fix MAJOR R23+2-B3：parsePackageIDsStrict 区分空 vs 非法
func TestParsePackageIDsStrict(t *testing.T) {
	cases := []struct {
		in       string
		wantOK   bool
		wantNil  bool
		wantLen  int
	}{
		{"", true, true, 0},               // 空 = 全适用
		{"  ", true, true, 0},             // 空白 = 全适用
		{"[]", true, false, 0},            // 空数组 = 合法
		{"[1,2,3]", true, false, 3},       // 合法
		{"not-json", false, true, 0},      // 损坏 → 拒绝
		{`["abc"]`, false, true, 0},       // 类型不匹配 → 拒绝
		{"null", true, true, 0},           // null 也是合法 JSON
	}
	for _, tc := range cases {
		ids, ok := parsePackageIDsStrict(tc.in)
		if ok != tc.wantOK {
			t.Errorf("in=%q got ok=%v want %v", tc.in, ok, tc.wantOK)
		}
		if (ids == nil) != tc.wantNil {
			t.Errorf("in=%q nil=%v want nil=%v", tc.in, ids == nil, tc.wantNil)
		}
		if len(ids) != tc.wantLen {
			t.Errorf("in=%q len=%d want %d", tc.in, len(ids), tc.wantLen)
		}
	}
}

// fix MAJOR R23+2-B3：损坏 snapshot 时 lockAndApplyCoupon fail-closed
func TestLockAndApplyCoupon_CorruptedSnapshotFailClosed(t *testing.T) {
	db := setupCouponTestDB(t)
	uc := database.UserCoupon{
		UserID: 1, Code: "CP-1-1-corrupt",
		Status: "available", SnapshotType: "fixed_price", SnapshotValue: 10,
		SnapshotPackageIDs: "not-json-corrupted",
	}
	db.Create(&uc)
	pkg := &database.Package{}
	pkg.ID = 5
	pkg.PriceAmount = 20

	err := db.Transaction(func(tx *gorm.DB) error {
		_, err := lockAndApplyCoupon(tx, 1, uc.ID, pkg)
		return err
	})
	if err == nil {
		t.Error("expected error when SnapshotPackageIDs is corrupted JSON (B3 fail-closed)")
	}
}

// fix MAJOR R23+2-B2：条件 UPDATE + RowsAffected — 并发抢占场景
func TestLockAndApplyCoupon_ConcurrentRace(t *testing.T) {
	db := setupCouponTestDB(t)
	uc := database.UserCoupon{
		UserID: 1, Code: "CP-1-1-race",
		Status: "available", SnapshotType: "fixed_price", SnapshotValue: 10,
	}
	db.Create(&uc)
	pkg := &database.Package{}
	pkg.ID = 5
	pkg.PriceAmount = 20

	// 第一笔事务消费成功
	err1 := db.Transaction(func(tx *gorm.DB) error {
		_, err := lockAndApplyCoupon(tx, 1, uc.ID, pkg)
		return err
	})
	if err1 != nil {
		t.Fatalf("first apply should succeed: %v", err1)
	}

	// 第二笔事务必须失败（DB 已是 'used'，条件 UPDATE RowsAffected=0）
	err2 := db.Transaction(func(tx *gorm.DB) error {
		_, err := lockAndApplyCoupon(tx, 1, uc.ID, pkg)
		return err
	})
	if err2 == nil {
		t.Error("second apply must fail (coupon already used by first tx)")
	}

	// 验证 DB 状态：UsedAt 只被第一次 set
	var fresh database.UserCoupon
	db.First(&fresh, uc.ID)
	if fresh.Status != "used" {
		t.Errorf("expected status=used got %q", fresh.Status)
	}
	if fresh.UsedAt == nil {
		t.Error("expected UsedAt set by first tx")
	}
}
