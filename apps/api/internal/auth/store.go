package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"
	"golang.org/x/crypto/bcrypt"
)

var (
	usersBucket        = []byte("users")
	usernameIdxBucket  = []byte("username_index")
	refreshTokenBucket = []byte("refresh_tokens")
	settingsBucket     = []byte("settings")
	// Buckets used by other packages (cluster management, etc.) —
	// initialized here so there's a single place where DB schema is defined.
	clustersBucket        = []byte("clusters")         // uploaded kubeconfigs
	clusterDisplayBucket  = []byte("cluster_display")  // display name overrides
)

// Role represents a KubeBolt application-level role.
type Role string

const (
	RoleAdmin  Role = "admin"
	RoleEditor Role = "editor"
	RoleViewer Role = "viewer"
)

// RoleLevel returns the numeric level for role comparison (higher = more permissions).
func RoleLevel(r Role) int {
	switch r {
	case RoleAdmin:
		return 3
	case RoleEditor:
		return 2
	case RoleViewer:
		return 1
	default:
		return 0
	}
}

// ValidRole checks if a role string is valid.
func ValidRole(r Role) bool {
	return r == RoleAdmin || r == RoleEditor || r == RoleViewer
}

// User represents a KubeBolt user stored in the database.
type User struct {
	ID           string     `json:"id"`
	Username     string     `json:"username"`
	Email        string     `json:"email"`
	Name         string     `json:"name"`
	PasswordHash string     `json:"passwordHash"`
	Role         Role       `json:"role"`
	CreatedAt    time.Time  `json:"createdAt"`
	UpdatedAt    time.Time  `json:"updatedAt"`
	LastLoginAt  *time.Time `json:"lastLoginAt,omitempty"`
}

// UserResponse is the API-safe representation of a User (no password hash).
type UserResponse struct {
	ID          string     `json:"id"`
	Username    string     `json:"username"`
	Email       string     `json:"email"`
	Name        string     `json:"name"`
	Role        Role       `json:"role"`
	CreatedAt   time.Time  `json:"createdAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`
	LastLoginAt *time.Time `json:"lastLoginAt,omitempty"`
}

// ToResponse converts a User to its API-safe representation.
func (u *User) ToResponse() UserResponse {
	return UserResponse{
		ID:          u.ID,
		Username:    u.Username,
		Email:       u.Email,
		Name:        u.Name,
		Role:        u.Role,
		CreatedAt:   u.CreatedAt,
		UpdatedAt:   u.UpdatedAt,
		LastLoginAt: u.LastLoginAt,
	}
}

// RefreshToken represents a stored refresh token.
type RefreshToken struct {
	TokenHash string    `json:"tokenHash"`
	UserID    string    `json:"userId"`
	ExpiresAt time.Time `json:"expiresAt"`
	CreatedAt time.Time `json:"createdAt"`
}

// Store manages user and token persistence with BoltDB.
type Store struct {
	db *bolt.DB
}

// NewStore opens (or creates) the BoltDB database and initializes buckets.
func NewStore(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}

	dbPath := filepath.Join(dataDir, "kubebolt.db")
	db, err := bolt.Open(dbPath, 0600, &bolt.Options{Timeout: 5 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	// Create buckets (auth + cross-package state like cluster management)
	err = db.Update(func(tx *bolt.Tx) error {
		for _, bucket := range [][]byte{usersBucket, usernameIdxBucket, refreshTokenBucket, settingsBucket, clustersBucket, clusterDisplayBucket} {
			if _, err := tx.CreateBucketIfNotExists(bucket); err != nil {
				return fmt.Errorf("create bucket %s: %w", bucket, err)
			}
		}
		return nil
	})
	if err != nil {
		db.Close()
		return nil, err
	}

	return &Store{db: db}, nil
}

// Close closes the underlying database.
func (s *Store) Close() error {
	return s.db.Close()
}

// DB returns the underlying BoltDB handle for use by other packages that
// share the same data file (e.g. cluster management). All buckets used
// across the codebase are created at Store initialization.
func (s *Store) DB() *bolt.DB {
	return s.db
}

// ClusterBuckets returns the bucket names used for cluster management state.
func ClusterBuckets() (configs, displayNames []byte) {
	return clustersBucket, clusterDisplayBucket
}

// CreateUser creates a new user with a bcrypt-hashed password.
func (s *Store) CreateUser(username, email, name, password string, role Role) (*User, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}

	now := time.Now().UTC()
	user := &User{
		ID:           uuid.New().String(),
		Username:     username,
		Email:        email,
		Name:         name,
		PasswordHash: string(hash),
		Role:         role,
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	err = s.db.Update(func(tx *bolt.Tx) error {
		// Check username uniqueness
		idx := tx.Bucket(usernameIdxBucket)
		if idx.Get([]byte(username)) != nil {
			return fmt.Errorf("username %q already exists", username)
		}

		data, err := json.Marshal(user)
		if err != nil {
			return err
		}

		if err := tx.Bucket(usersBucket).Put([]byte(user.ID), data); err != nil {
			return err
		}
		return idx.Put([]byte(username), []byte(user.ID))
	})
	if err != nil {
		return nil, err
	}
	return user, nil
}

// GetUser retrieves a user by ID.
func (s *Store) GetUser(id string) (*User, error) {
	var user User
	err := s.db.View(func(tx *bolt.Tx) error {
		data := tx.Bucket(usersBucket).Get([]byte(id))
		if data == nil {
			return fmt.Errorf("user not found")
		}
		return json.Unmarshal(data, &user)
	})
	if err != nil {
		return nil, err
	}
	return &user, nil
}

// GetUserByUsername retrieves a user by username via the index.
func (s *Store) GetUserByUsername(username string) (*User, error) {
	var user User
	err := s.db.View(func(tx *bolt.Tx) error {
		id := tx.Bucket(usernameIdxBucket).Get([]byte(username))
		if id == nil {
			return fmt.Errorf("user not found")
		}
		data := tx.Bucket(usersBucket).Get(id)
		if data == nil {
			return fmt.Errorf("user not found")
		}
		return json.Unmarshal(data, &user)
	})
	if err != nil {
		return nil, err
	}
	return &user, nil
}

// ListUsers returns all users in the store.
func (s *Store) ListUsers() ([]User, error) {
	var users []User
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(usersBucket).ForEach(func(k, v []byte) error {
			var u User
			if err := json.Unmarshal(v, &u); err != nil {
				return err
			}
			users = append(users, u)
			return nil
		})
	})
	return users, err
}

// UpdateUser updates a user's mutable fields (username, email, name, role).
func (s *Store) UpdateUser(id, username, email, name string, role Role) (*User, error) {
	var updated User
	err := s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(usersBucket)
		data := bucket.Get([]byte(id))
		if data == nil {
			return fmt.Errorf("user not found")
		}

		var user User
		if err := json.Unmarshal(data, &user); err != nil {
			return err
		}

		// If username changed, update index
		if username != "" && username != user.Username {
			idx := tx.Bucket(usernameIdxBucket)
			if existing := idx.Get([]byte(username)); existing != nil {
				return fmt.Errorf("username %q already exists", username)
			}
			idx.Delete([]byte(user.Username))
			idx.Put([]byte(username), []byte(id))
			user.Username = username
		}

		if email != "" {
			user.Email = email
		}
		if name != "" {
			user.Name = name
		}
		if role != "" && ValidRole(role) {
			user.Role = role
		}
		user.UpdatedAt = time.Now().UTC()

		newData, err := json.Marshal(&user)
		if err != nil {
			return err
		}
		updated = user
		return bucket.Put([]byte(id), newData)
	})
	if err != nil {
		return nil, err
	}
	return &updated, nil
}

// UpdatePassword changes a user's password.
func (s *Store) UpdatePassword(id, newPassword string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), 12)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}

	return s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(usersBucket)
		data := bucket.Get([]byte(id))
		if data == nil {
			return fmt.Errorf("user not found")
		}

		var user User
		if err := json.Unmarshal(data, &user); err != nil {
			return err
		}

		user.PasswordHash = string(hash)
		user.UpdatedAt = time.Now().UTC()

		newData, err := json.Marshal(&user)
		if err != nil {
			return err
		}
		return bucket.Put([]byte(id), newData)
	})
}

// UpdateLastLogin sets the last login timestamp for a user.
func (s *Store) UpdateLastLogin(id string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(usersBucket)
		data := bucket.Get([]byte(id))
		if data == nil {
			return fmt.Errorf("user not found")
		}

		var user User
		if err := json.Unmarshal(data, &user); err != nil {
			return err
		}

		now := time.Now().UTC()
		user.LastLoginAt = &now
		user.UpdatedAt = now

		newData, err := json.Marshal(&user)
		if err != nil {
			return err
		}
		return bucket.Put([]byte(id), newData)
	})
}

// DeleteUser removes a user and their username index entry.
func (s *Store) DeleteUser(id string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(usersBucket)
		data := bucket.Get([]byte(id))
		if data == nil {
			return fmt.Errorf("user not found")
		}

		var user User
		if err := json.Unmarshal(data, &user); err != nil {
			return err
		}

		if err := tx.Bucket(usernameIdxBucket).Delete([]byte(user.Username)); err != nil {
			return err
		}
		// Also delete refresh tokens for this user
		rtBucket := tx.Bucket(refreshTokenBucket)
		var tokensToDelete [][]byte
		rtBucket.ForEach(func(k, v []byte) error {
			var rt RefreshToken
			if err := json.Unmarshal(v, &rt); err == nil && rt.UserID == id {
				tokensToDelete = append(tokensToDelete, k)
			}
			return nil
		})
		for _, k := range tokensToDelete {
			rtBucket.Delete(k)
		}

		return bucket.Delete([]byte(id))
	})
}

// UserCount returns the total number of users.
func (s *Store) UserCount() (int, error) {
	var count int
	err := s.db.View(func(tx *bolt.Tx) error {
		count = tx.Bucket(usersBucket).Stats().KeyN
		return nil
	})
	return count, err
}

// CountByRole returns the number of users with the given role.
func (s *Store) CountByRole(role Role) (int, error) {
	var count int
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(usersBucket).ForEach(func(k, v []byte) error {
			var u User
			if err := json.Unmarshal(v, &u); err != nil {
				return nil // skip corrupt entries
			}
			if u.Role == role {
				count++
			}
			return nil
		})
	})
	return count, err
}

// SeedAdmin creates the default admin user if no users exist.
func (s *Store) SeedAdmin(password string) (bool, error) {
	count, err := s.UserCount()
	if err != nil {
		return false, err
	}
	if count > 0 {
		return false, nil
	}

	_, err = s.CreateUser("admin", "admin@localhost", "Admin", password, RoleAdmin)
	if err != nil {
		return false, fmt.Errorf("seed admin user: %w", err)
	}
	return true, nil
}

// SaveRefreshToken stores a refresh token.
func (s *Store) SaveRefreshToken(rt *RefreshToken) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		data, err := json.Marshal(rt)
		if err != nil {
			return err
		}
		return tx.Bucket(refreshTokenBucket).Put([]byte(rt.TokenHash), data)
	})
}

// GetRefreshToken retrieves a refresh token by its hash.
func (s *Store) GetRefreshToken(tokenHash string) (*RefreshToken, error) {
	var rt RefreshToken
	err := s.db.View(func(tx *bolt.Tx) error {
		data := tx.Bucket(refreshTokenBucket).Get([]byte(tokenHash))
		if data == nil {
			return fmt.Errorf("refresh token not found")
		}
		return json.Unmarshal(data, &rt)
	})
	if err != nil {
		return nil, err
	}
	return &rt, nil
}

// DeleteRefreshToken removes a refresh token by its hash.
func (s *Store) DeleteRefreshToken(tokenHash string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(refreshTokenBucket).Delete([]byte(tokenHash))
	})
}

// DeleteUserRefreshTokens removes all refresh tokens for a user.
func (s *Store) DeleteUserRefreshTokens(userID string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(refreshTokenBucket)
		var toDelete [][]byte
		bucket.ForEach(func(k, v []byte) error {
			var rt RefreshToken
			if err := json.Unmarshal(v, &rt); err == nil && rt.UserID == userID {
				toDelete = append(toDelete, k)
			}
			return nil
		})
		for _, k := range toDelete {
			bucket.Delete(k)
		}
		return nil
	})
}

// GetSetting retrieves a setting value by key from the settings bucket.
func (s *Store) GetSetting(key string) ([]byte, error) {
	var val []byte
	err := s.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket(settingsBucket).Get([]byte(key))
		if v == nil {
			return fmt.Errorf("setting %q not found", key)
		}
		val = make([]byte, len(v))
		copy(val, v)
		return nil
	})
	return val, err
}

// SetSetting stores a setting value by key in the settings bucket.
func (s *Store) SetSetting(key string, value []byte) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(settingsBucket).Put([]byte(key), value)
	})
}

// CheckPassword verifies a plaintext password against a user's stored hash.
func CheckPassword(user *User, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)) == nil
}
