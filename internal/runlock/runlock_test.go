package runlock

import "testing"

func TestProfileLockPreventsOverlap(t *testing.T) {
	directory := t.TempDir()
	first, acquired, err := Acquire(directory, "production")
	if err != nil {
		t.Fatal(err)
	}
	if !acquired {
		t.Fatal("first lock was not acquired")
	}
	defer first.Release()

	second, acquired, err := Acquire(directory, "production")
	if err != nil {
		t.Fatal(err)
	}
	if acquired {
		_ = second.Release()
		t.Fatal("overlapping profile lock was acquired")
	}
}

func TestIndependentProfilesDoNotBlockEachOther(t *testing.T) {
	directory := t.TempDir()
	first, acquired, err := Acquire(directory, "production")
	if err != nil || !acquired {
		t.Fatalf("acquire first profile: acquired=%v err=%v", acquired, err)
	}
	defer first.Release()

	second, acquired, err := Acquire(directory, "staging")
	if err != nil || !acquired {
		t.Fatalf("independent profile was blocked: acquired=%v err=%v", acquired, err)
	}
	defer second.Release()
}

func TestIsActiveDistinguishesHeldAndReleasedLocks(t *testing.T) {
	directory := t.TempDir()
	active, err := IsActive(directory, "production")
	if err != nil {
		t.Fatal(err)
	}
	if active {
		t.Fatal("unheld lock reported as active")
	}

	lock, acquired, err := Acquire(directory, "production")
	if err != nil || !acquired {
		t.Fatalf("acquire lock: acquired=%v err=%v", acquired, err)
	}
	active, err = IsActive(directory, "production")
	if err != nil {
		t.Fatal(err)
	}
	if !active {
		t.Fatal("held lock reported as inactive")
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
	active, err = IsActive(directory, "production")
	if err != nil {
		t.Fatal(err)
	}
	if active {
		t.Fatal("released lock reported as active")
	}
}
