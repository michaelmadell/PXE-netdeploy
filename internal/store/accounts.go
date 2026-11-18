package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Admin accounts, sessions and API tokens (ADR 0029).

var (
	// ErrUsernameTaken: another account has this username, ignoring case.
	ErrUsernameTaken = errors.New("store: username taken")
	// ErrLastUser: the only account can't be deleted.
	ErrLastUser = errors.New("store: the last account can't be deleted")
	// ErrAlreadySetUp: the first account exists.
	ErrAlreadySetUp = errors.New("store: an account already exists")
)

// User is a users row.
type User struct {
	ID                string
	Username          string
	PasswordHash      string
	CreatedAt         time.Time
	LastSignInAt      *time.Time
	PasswordChangedAt *time.Time
}

const userCols = `id, username, password_hash, created_at, last_sign_in_at, password_changed_at`

func scanUser(row interface{ Scan(...any) error }) (User, error) {
	var u User
	var created int64
	var signIn, changed sql.NullInt64
	if err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &created, &signIn, &changed); err != nil {
		return User{}, err
	}
	u.CreatedAt, u.LastSignInAt, u.PasswordChangedAt = fromMS(created), fromNullMS(signIn), fromNullMS(changed)
	return u, nil
}

// CountUsers returns how many accounts exist.
func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	if err := s.r.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: count users: %w", err)
	}
	return n, nil
}

// CreateUser adds an account. ErrUsernameTaken if the name is in use.
func (s *Store) CreateUser(ctx context.Context, username, passwordHash string) (User, error) {
	return s.createUser(ctx, username, passwordHash, false)
}

// CreateFirstUser adds an account only while there is none
// (ErrAlreadySetUp otherwise).
func (s *Store) CreateFirstUser(ctx context.Context, username, passwordHash string) (User, error) {
	return s.createUser(ctx, username, passwordHash, true)
}

func (s *Store) createUser(ctx context.Context, username, passwordHash string, first bool) (User, error) {
	u := User{ID: newID(), Username: username, PasswordHash: passwordHash, CreatedAt: fromMS(ms(s.now()))}
	err := s.tx(ctx, func(tx *sql.Tx) error {
		if first {
			var n int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&n); err != nil {
				return err
			}
			if n > 0 {
				return ErrAlreadySetUp
			}
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO users (id, username, password_hash, created_at) VALUES (?, ?, ?, ?)`,
			u.ID, u.Username, u.PasswordHash, ms(u.CreatedAt))
		return err
	})
	switch {
	case errors.Is(err, ErrAlreadySetUp):
		return User{}, err
	case uniqueViolation(err, ""):
		return User{}, ErrUsernameTaken
	case err != nil:
		return User{}, fmt.Errorf("store: create user: %w", err)
	}
	return u, nil
}

// Users returns every account by username.
func (s *Store) Users(ctx context.Context) ([]User, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT `+userCols+` FROM users ORDER BY username COLLATE NOCASE`)
	if err != nil {
		return nil, fmt.Errorf("store: users: %w", err)
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, fmt.Errorf("store: users: %w", err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// UserByID returns an account; ErrNotFound if there is none.
func (s *Store) UserByID(ctx context.Context, id string) (User, error) {
	return s.user(ctx, `id = ?`, id)
}

// UserByName returns an account by username, ignoring case.
func (s *Store) UserByName(ctx context.Context, username string) (User, error) {
	return s.user(ctx, `username = ? COLLATE NOCASE`, username)
}

func (s *Store) user(ctx context.Context, where string, arg any) (User, error) {
	u, err := scanUser(s.r.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE `+where, arg))
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("store: user: %w", err)
	}
	return u, nil
}

// DeleteUser deletes an account with its sessions and tokens. ErrLastUser
// for the only account.
func (s *Store) DeleteUser(ctx context.Context, id string) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&n); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM users WHERE id = ? AND ? > 1`, id, n)
		if err != nil {
			return fmt.Errorf("store: delete user: %w", err)
		}
		if c, _ := res.RowsAffected(); c == 0 {
			var exists int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE id = ?`, id).Scan(&exists); err != nil {
				return err
			}
			if exists == 0 {
				return ErrNotFound
			}
			return ErrLastUser
		}
		return nil
	})
}

// SetPassword replaces an account's password hash and ends its sessions,
// except keepSession (a session hash; "" ends them all).
func (s *Store) SetPassword(ctx context.Context, id, passwordHash, keepSession string) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE users SET password_hash = ?, password_changed_at = ? WHERE id = ?`, passwordHash, ms(s.now()), id)
		if err != nil {
			return fmt.Errorf("store: set password: %w", err)
		}
		if c, _ := res.RowsAffected(); c == 0 {
			return ErrNotFound
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM admin_sessions WHERE user_id = ? AND hash <> ?`, id, keepSession)
		return err
	})
}

// RecordSignIn sets an account's last sign-in time.
func (s *Store) RecordSignIn(ctx context.Context, id string) error {
	_, err := s.w.ExecContext(ctx, `UPDATE users SET last_sign_in_at = ? WHERE id = ?`, ms(s.now()), id)
	return err
}

// ---------------------------------------------------------------- sessions

// AdminSession is an admin_sessions row with its account's name.
type AdminSession struct {
	Hash       string
	UserID     string
	Username   string
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time
}

// CreateAdminSession stores a session by its hash.
func (s *Store) CreateAdminSession(ctx context.Context, hash, userID string, expires time.Time) error {
	now := ms(s.now())
	_, err := s.w.ExecContext(ctx, `INSERT INTO admin_sessions (hash, user_id, created_at, last_seen_at, expires_at) VALUES (?, ?, ?, ?, ?)`,
		hash, userID, now, now, ms(expires))
	if err != nil {
		return fmt.Errorf("store: create session: %w", err)
	}
	return nil
}

// AdminSessionByHash returns a session whatever its age; ErrNotFound.
func (s *Store) AdminSessionByHash(ctx context.Context, hash string) (AdminSession, error) {
	var x AdminSession
	var created, seen, exp int64
	err := s.r.QueryRowContext(ctx, `SELECT s.hash, s.user_id, u.username, s.created_at, s.last_seen_at, s.expires_at
		FROM admin_sessions s JOIN users u ON u.id = s.user_id WHERE s.hash = ?`, hash).
		Scan(&x.Hash, &x.UserID, &x.Username, &created, &seen, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return x, ErrNotFound
	}
	if err != nil {
		return x, fmt.Errorf("store: session: %w", err)
	}
	x.CreatedAt, x.LastSeenAt, x.ExpiresAt = fromMS(created), fromMS(seen), fromMS(exp)
	return x, nil
}

// TouchAdminSession records activity on a session.
func (s *Store) TouchAdminSession(ctx context.Context, hash string) error {
	_, err := s.w.ExecContext(ctx, `UPDATE admin_sessions SET last_seen_at = ? WHERE hash = ?`, ms(s.now()), hash)
	return err
}

// DeleteAdminSession ends a session.
func (s *Store) DeleteAdminSession(ctx context.Context, hash string) error {
	_, err := s.w.ExecContext(ctx, `DELETE FROM admin_sessions WHERE hash = ?`, hash)
	return err
}

// PurgeAdminSessions deletes sessions past their expiry or idle for longer
// than idle.
func (s *Store) PurgeAdminSessions(ctx context.Context, idle time.Duration) (int64, error) {
	now := s.now()
	res, err := s.w.ExecContext(ctx, `DELETE FROM admin_sessions WHERE expires_at <= ? OR last_seen_at <= ?`, ms(now), ms(now.Add(-idle)))
	if err != nil {
		return 0, fmt.Errorf("store: purge sessions: %w", err)
	}
	return res.RowsAffected()
}

// ---------------------------------------------------------------- tokens

// APIToken is an api_tokens row with its creator's name.
type APIToken struct {
	ID         string
	Name       string
	UserID     string
	Username   string
	CreatedAt  time.Time
	ExpiresAt  *time.Time
	LastUsedAt *time.Time
}

const tokenSelect = `SELECT t.id, t.name, t.user_id, u.username, t.created_at, t.expires_at, t.last_used_at
	FROM api_tokens t JOIN users u ON u.id = t.user_id`

func scanToken(row interface{ Scan(...any) error }) (APIToken, error) {
	var t APIToken
	var created int64
	var exp, used sql.NullInt64
	if err := row.Scan(&t.ID, &t.Name, &t.UserID, &t.Username, &created, &exp, &used); err != nil {
		return APIToken{}, err
	}
	t.CreatedAt, t.ExpiresAt, t.LastUsedAt = fromMS(created), fromNullMS(exp), fromNullMS(used)
	return t, nil
}

// CreateAPIToken stores a token by its hash.
func (s *Store) CreateAPIToken(ctx context.Context, hash, name, userID string, expires *time.Time) (APIToken, error) {
	t := APIToken{ID: newID(), Name: name, UserID: userID, CreatedAt: fromMS(ms(s.now())), ExpiresAt: expires}
	if _, err := s.w.ExecContext(ctx, `INSERT INTO api_tokens (id, hash, name, user_id, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?)`,
		t.ID, hash, name, userID, ms(t.CreatedAt), nullMS(expires)); err != nil {
		if fkViolation(err) {
			return APIToken{}, ErrNotFound
		}
		return APIToken{}, fmt.Errorf("store: create token: %w", err)
	}
	u, err := s.UserByID(ctx, userID)
	if err != nil {
		return APIToken{}, err
	}
	t.Username = u.Username
	return t, nil
}

// APITokens returns every token, newest first.
func (s *Store) APITokens(ctx context.Context) ([]APIToken, error) {
	rows, err := s.r.QueryContext(ctx, tokenSelect+` ORDER BY t.created_at DESC, t.id DESC`)
	if err != nil {
		return nil, fmt.Errorf("store: tokens: %w", err)
	}
	defer rows.Close()
	var out []APIToken
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, fmt.Errorf("store: tokens: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// APITokenByHash returns a token whatever its expiry; ErrNotFound.
func (s *Store) APITokenByHash(ctx context.Context, hash string) (APIToken, error) {
	t, err := scanToken(s.r.QueryRowContext(ctx, tokenSelect+` WHERE t.hash = ?`, hash))
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrNotFound
	}
	if err != nil {
		return t, fmt.Errorf("store: token: %w", err)
	}
	return t, nil
}

// TouchAPIToken records a use.
func (s *Store) TouchAPIToken(ctx context.Context, id string) error {
	_, err := s.w.ExecContext(ctx, `UPDATE api_tokens SET last_used_at = ? WHERE id = ?`, ms(s.now()), id)
	return err
}

// DeleteAPIToken revokes a token; ErrNotFound.
func (s *Store) DeleteAPIToken(ctx context.Context, id string) error {
	res, err := s.w.ExecContext(ctx, `DELETE FROM api_tokens WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: delete token: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
