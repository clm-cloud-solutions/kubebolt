package auth

import (
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestRoleLevel_Hierarchy(t *testing.T) {
	if RoleLevel(RoleAdmin) <= RoleLevel(RoleEditor) {
		t.Error("admin should outrank editor")
	}
	if RoleLevel(RoleEditor) <= RoleLevel(RoleViewer) {
		t.Error("editor should outrank viewer")
	}
	if RoleLevel(RoleViewer) <= 0 {
		t.Error("viewer should have positive level")
	}
	if RoleLevel(Role("bogus")) != 0 {
		t.Error("unknown role should return 0")
	}
}

func TestValidRole(t *testing.T) {
	if !ValidRole(RoleAdmin) || !ValidRole(RoleEditor) || !ValidRole(RoleViewer) {
		t.Error("canonical roles must validate")
	}
	if ValidRole(Role("superuser")) {
		t.Error("unknown role must not validate")
	}
}

func TestCreateUser_HashesPassword(t *testing.T) {
	store := newTestStore(t)
	u, err := store.CreateUser("alice", "a@x", "Alice", "supersecret", RoleEditor)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if u.PasswordHash == "supersecret" || u.PasswordHash == "" {
		t.Errorf("password should be hashed, got %q", u.PasswordHash)
	}
	// bcrypt compare must succeed
	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte("supersecret")); err != nil {
		t.Errorf("bcrypt compare failed: %v", err)
	}
	// Wrong password must fail
	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte("nope")); err == nil {
		t.Error("wrong password should fail")
	}
}

func TestCreateUser_RejectsDuplicate(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.CreateUser("alice", "a@x", "Alice", "p1", RoleViewer); err != nil {
		t.Fatalf("first create: %v", err)
	}
	if _, err := store.CreateUser("alice", "b@y", "Alice 2", "p2", RoleViewer); err == nil {
		t.Error("duplicate username should fail")
	}
}

func TestGetUser_ByIDAndUsername(t *testing.T) {
	store := newTestStore(t)
	u, _ := store.CreateUser("bob", "b@x", "Bob", "pwd12345", RoleAdmin)

	got, err := store.GetUser(u.ID)
	if err != nil || got.Username != "bob" {
		t.Errorf("GetUser failed: %v / %+v", err, got)
	}

	got2, err := store.GetUserByUsername("bob")
	if err != nil || got2.ID != u.ID {
		t.Errorf("GetUserByUsername failed: %v / %+v", err, got2)
	}

	if _, err := store.GetUser("nope"); err == nil {
		t.Error("GetUser of missing id should fail")
	}
	if _, err := store.GetUserByUsername("ghost"); err == nil {
		t.Error("GetUserByUsername of missing name should fail")
	}
}

func TestListUsers(t *testing.T) {
	store := newTestStore(t)
	store.CreateUser("a", "a@x", "A", "p12345678", RoleAdmin)
	store.CreateUser("b", "b@x", "B", "p12345678", RoleEditor)
	store.CreateUser("c", "c@x", "C", "p12345678", RoleViewer)

	users, err := store.ListUsers()
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if len(users) != 3 {
		t.Errorf("want 3 users, got %d", len(users))
	}
}

func TestUpdatePassword(t *testing.T) {
	store := newTestStore(t)
	u, _ := store.CreateUser("carol", "c@x", "Carol", "oldpassword", RoleViewer)
	if err := store.UpdatePassword(u.ID, "newpassword"); err != nil {
		t.Fatalf("UpdatePassword: %v", err)
	}
	got, _ := store.GetUser(u.ID)
	if bcrypt.CompareHashAndPassword([]byte(got.PasswordHash), []byte("oldpassword")) == nil {
		t.Error("old password should no longer match")
	}
	if bcrypt.CompareHashAndPassword([]byte(got.PasswordHash), []byte("newpassword")) != nil {
		t.Error("new password should match")
	}
}

func TestDeleteUser(t *testing.T) {
	store := newTestStore(t)
	u, _ := store.CreateUser("dave", "d@x", "Dave", "p12345678", RoleViewer)
	if err := store.DeleteUser(u.ID); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	if _, err := store.GetUser(u.ID); err == nil {
		t.Error("user should be gone after delete")
	}
	// Username index also cleared → a new user with the same name can be created
	if _, err := store.CreateUser("dave", "d2@x", "Dave 2", "p12345678", RoleViewer); err != nil {
		t.Errorf("username should be reusable after delete: %v", err)
	}
}

func TestCountByRole(t *testing.T) {
	store := newTestStore(t)
	store.CreateUser("a1", "a1@x", "", "p12345678", RoleAdmin)
	store.CreateUser("a2", "a2@x", "", "p12345678", RoleAdmin)
	store.CreateUser("e1", "e1@x", "", "p12345678", RoleEditor)

	n, _ := store.CountByRole(RoleAdmin)
	if n != 2 {
		t.Errorf("CountByRole(admin) = %d, want 2", n)
	}
	n, _ = store.CountByRole(RoleViewer)
	if n != 0 {
		t.Errorf("CountByRole(viewer) = %d, want 0", n)
	}
}

func TestSeedAdmin_OnlyWhenEmpty(t *testing.T) {
	store := newTestStore(t)

	seeded, err := store.SeedAdmin("admin123")
	if err != nil || !seeded {
		t.Fatalf("initial SeedAdmin: seeded=%v err=%v", seeded, err)
	}
	if n, _ := store.UserCount(); n != 1 {
		t.Errorf("want 1 user after seed, got %d", n)
	}
	// Second call is a no-op
	seeded, err = store.SeedAdmin("different")
	if err != nil {
		t.Fatalf("second SeedAdmin: %v", err)
	}
	if seeded {
		t.Error("SeedAdmin should no-op when users exist")
	}
}

func TestSettings_Persistence(t *testing.T) {
	store := newTestStore(t)
	if err := store.SetSetting("key1", []byte("value1")); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	got, err := store.GetSetting("key1")
	if err != nil {
		t.Fatalf("GetSetting: %v", err)
	}
	if string(got) != "value1" {
		t.Errorf("roundtrip mismatch: got %q", got)
	}

	// Missing key
	if _, err := store.GetSetting("missing"); err == nil {
		t.Error("missing key should return error")
	}
}
