package storage

import (
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newTestDB(t *testing.T) *DB {
	t.Helper()
	f, err := os.CreateTemp("", "relay-test-*.db")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	t.Cleanup(func() { os.Remove(f.Name()) })
	db, err := Open(f.Name(), "testpepper", "testdevicesecret")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestCodeLifecycle(t *testing.T) {
	db := newTestDB(t)

	id, plain, err := db.CreateCode("test", time.Hour)
	if err != nil {
		t.Fatalf("CreateCode: %v", err)
	}

	codes, err := db.ListCodes()
	if err != nil {
		t.Fatalf("ListCodes: %v", err)
	}
	if len(codes) != 1 || codes[0].ID != id {
		t.Fatalf("expected 1 code with id %s, got %v", id, codes)
	}
	if codes[0].UsedAt != nil {
		t.Fatal("expected UsedAt nil before redemption")
	}

	dev, token, err := db.RedeemCode(plain, "fp1", "iPhone 15")
	if err != nil {
		t.Fatalf("RedeemCode: %v", err)
	}
	if dev == nil || token == "" {
		t.Fatal("expected device and token")
	}

	codes, err = db.ListCodes()
	if err != nil {
		t.Fatalf("ListCodes after redeem: %v", err)
	}
	if codes[0].UsedAt == nil {
		t.Fatal("expected UsedAt set after redemption")
	}
}

func TestCodeExpiry(t *testing.T) {
	db := newTestDB(t)
	_, plain, err := db.CreateCode("expiry", time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	_, _, err = db.RedeemCode(plain, "fp1", "device")
	if err != ErrCodeInvalid {
		t.Fatalf("expected ErrCodeInvalid, got %v", err)
	}
}

func TestCodeRevoke(t *testing.T) {
	db := newTestDB(t)
	id, plain, err := db.CreateCode("revoke", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.RevokeCode(id); err != nil {
		t.Fatal(err)
	}
	_, _, err = db.RedeemCode(plain, "fp1", "device")
	if err != ErrCodeInvalid {
		t.Fatalf("expected ErrCodeInvalid after revoke, got %v", err)
	}
}

func TestAtomicRedemption(t *testing.T) {
	db := newTestDB(t)
	_, plain, err := db.CreateCode("race", time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	var successCount atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := db.RedeemCode(plain, "fp-race", "racer")
			if err == nil {
				successCount.Add(1)
			}
		}()
	}
	wg.Wait()

	if n := successCount.Load(); n != 1 {
		t.Fatalf("expected exactly 1 successful redemption, got %d", n)
	}
}

func TestTokenRoundtrip(t *testing.T) {
	db := newTestDB(t)
	_, plain, err := db.CreateCode("token", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	dev, token, err := db.RedeemCode(plain, "fp2", "device2")
	if err != nil {
		t.Fatal(err)
	}

	dev2, err := db.ValidateToken(token)
	if err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}
	if dev2.ID != dev.ID {
		t.Fatalf("expected device ID %s, got %s", dev.ID, dev2.ID)
	}
}

func TestMigrationsIdempotent(t *testing.T) {
	f, err := os.CreateTemp("", "relay-migrate-*.db")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	defer os.Remove(f.Name())

	db1, err := Open(f.Name(), "p", "s")
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	db1.Close()

	db2, err := Open(f.Name(), "p", "s")
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	db2.Close()
}

func TestRevokeDevice(t *testing.T) {
	db := newTestDB(t)
	_, plain, err := db.CreateCode("dev", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	dev, token, err := db.RedeemCode(plain, "fp3", "device3")
	if err != nil {
		t.Fatal(err)
	}

	if err := db.RevokeDevice(dev.ID); err != nil {
		t.Fatalf("RevokeDevice: %v", err)
	}

	_, err = db.ValidateToken(token)
	if err == nil {
		t.Fatal("expected error for revoked device token")
	}
}

func TestUpdateLastSeen(t *testing.T) {
	db := newTestDB(t)
	_, plain, err := db.CreateCode("lastseen", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	dev, _, err := db.RedeemCode(plain, "fp4", "device4")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateLastSeen(dev.ID); err != nil {
		t.Fatalf("UpdateLastSeen: %v", err)
	}
}
