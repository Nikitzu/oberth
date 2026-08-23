package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/oberthci/oberth/internal/model"
)

// UplinkStatus pairs one uplink with the usage state of its bound token
// credential for administrative listings. LastUsedAt is zero when the bearer
// token has never authenticated.
type UplinkStatus struct {
	model.Uplink
	LastUsedAt time.Time
}

// RegisterUpstream atomically creates operator configuration and its audit
// record. Administrative commands must not leave unaudited live mutations.
func (s *Store) RegisterUpstream(ctx context.Context, actor string, spec model.UpstreamSpec) (model.Upstream, error) {
	if strings.TrimSpace(actor) == "" || strings.TrimSpace(spec.Name) == "" || strings.TrimSpace(spec.Kind) == "" || strings.TrimSpace(spec.BaseURL) == "" {
		return model.Upstream{}, fmt.Errorf("%w: actor and upstream fields are required", ErrInvalid)
	}
	details, err := json.Marshal(map[string]string{"name": spec.Name, "kind": spec.Kind, "base_url": spec.BaseURL, "key_name": spec.KeyName})
	if err != nil {
		return model.Upstream{}, fmt.Errorf("encode upstream audit: %w", err)
	}
	now := unixNano(s.now())
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return model.Upstream{}, fmt.Errorf("begin upstream registration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `
INSERT INTO upstreams(name, kind, base_url, key_name, created_at, updated_at) VALUES(?, ?, ?, ?, ?, ?)`,
		spec.Name, spec.Kind, spec.BaseURL, spec.KeyName, now, now)
	if err != nil {
		return model.Upstream{}, fmt.Errorf("register upstream: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return model.Upstream{}, fmt.Errorf("read upstream id: %w", err)
	}
	if _, err := appendAuditAction(ctx, tx, model.AuditActionSpec{
		Actor: actor, Action: "upstream.register", ResourceType: "upstream",
		ResourceID: fmt.Sprint(id), Details: string(details),
	}, now); err != nil {
		return model.Upstream{}, fmt.Errorf("audit upstream registration: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return model.Upstream{}, fmt.Errorf("commit upstream registration: %w", err)
	}
	return s.Upstream(ctx, id)
}

// RegisterRepository atomically maps one repository name to a configured
// upstream and records the audit action. Push-time discovery creates this
// mapping automatically only while exactly one upstream is configured; on a
// multi-upstream deployment an administrator declares the mapping explicitly
// before the repository's first push.
func (s *Store) RegisterRepository(ctx context.Context, actor string, spec model.RepositorySpec) (model.Repository, error) {
	if strings.TrimSpace(actor) == "" || strings.TrimSpace(spec.Name) == "" || spec.UpstreamID <= 0 || strings.TrimSpace(spec.DefaultBranch) == "" {
		return model.Repository{}, fmt.Errorf("%w: actor and repository fields are required", ErrInvalid)
	}
	details, err := json.Marshal(map[string]string{
		"name": spec.Name, "upstream_id": fmt.Sprint(spec.UpstreamID), "default_branch": spec.DefaultBranch,
	})
	if err != nil {
		return model.Repository{}, fmt.Errorf("encode repository audit: %w", err)
	}
	now := unixNano(s.now())
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return model.Repository{}, fmt.Errorf("begin repository registration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `
INSERT INTO repositories(name, upstream_id, default_branch, created_at, updated_at) VALUES(?, ?, ?, ?, ?)`,
		spec.Name, spec.UpstreamID, spec.DefaultBranch, now, now)
	if err != nil {
		return model.Repository{}, fmt.Errorf("register repository: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return model.Repository{}, fmt.Errorf("read repository id: %w", err)
	}
	if _, err := appendAuditAction(ctx, tx, model.AuditActionSpec{
		Actor: actor, Action: "repository.register", ResourceType: "repository",
		ResourceID: fmt.Sprint(id), Details: string(details),
	}, now); err != nil {
		return model.Repository{}, fmt.Errorf("audit repository registration: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return model.Repository{}, fmt.Errorf("commit repository registration: %w", err)
	}
	return s.Repository(ctx, id)
}

// RemoveUpstream atomically removes one upstream and its mapped repositories,
// with the audit record, and returns the removed repository names. A mapped
// repository that already holds CI history (runs, promotions, receive events,
// publications, or issues reference it) fails the removal closed: history rows
// are immutable evidence and are never cascaded away.
func (s *Store) RemoveUpstream(ctx context.Context, actor, name string) ([]string, error) {
	if strings.TrimSpace(actor) == "" || strings.TrimSpace(name) == "" {
		return nil, fmt.Errorf("%w: actor and upstream name are required", ErrInvalid)
	}
	now := unixNano(s.now())
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin upstream removal: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var upstreamID int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM upstreams WHERE name = ?`, name).Scan(&upstreamID); err != nil {
		return nil, translateNotFound("upstream", err)
	}
	repositories, err := listUpstreamRepositoryNames(ctx, tx, upstreamID)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM repositories WHERE upstream_id = ?`, upstreamID); err != nil {
		if strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
			return nil, fmt.Errorf("upstream %s has repositories with immutable CI history; removal is not supported once runs exist", name)
		}
		return nil, fmt.Errorf("remove upstream repositories: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM upstreams WHERE id = ?`, upstreamID); err != nil {
		return nil, fmt.Errorf("remove upstream: %w", err)
	}
	details, err := json.Marshal(map[string]string{"name": name, "removed_repositories": strings.Join(repositories, ",")})
	if err != nil {
		return nil, fmt.Errorf("encode upstream removal audit: %w", err)
	}
	if _, err := appendAuditAction(ctx, tx, model.AuditActionSpec{
		Actor: actor, Action: "upstream.remove", ResourceType: "upstream",
		ResourceID: fmt.Sprint(upstreamID), Details: string(details),
	}, now); err != nil {
		return nil, fmt.Errorf("audit upstream removal: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit upstream removal: %w", err)
	}
	return repositories, nil
}

func listUpstreamRepositoryNames(ctx context.Context, tx *sql.Tx, upstreamID int64) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT name FROM repositories WHERE upstream_id = ? ORDER BY name`, upstreamID)
	if err != nil {
		return nil, fmt.Errorf("list upstream repositories: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var repositories []string
	for rows.Next() {
		var repositoryName string
		if err := rows.Scan(&repositoryName); err != nil {
			return nil, fmt.Errorf("scan upstream repository: %w", err)
		}
		repositories = append(repositories, repositoryName)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list upstream repositories: %w", err)
	}
	return repositories, nil
}

func matchUplinksByIdentity(ctx context.Context, tx *sql.Tx, identity, fingerprint string) ([]model.Uplink, error) {
	query := `SELECT id, fingerprint, identity, token_credential_id, auth_actor, admin, created_at, updated_at FROM uplinks WHERE identity = ?`
	arguments := []any{identity}
	if strings.TrimSpace(fingerprint) != "" {
		query += ` AND fingerprint = ?`
		arguments = append(arguments, fingerprint)
	}
	rows, err := tx.QueryContext(ctx, query+` ORDER BY fingerprint`, arguments...)
	if err != nil {
		return nil, fmt.Errorf("look up uplink: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var matches []model.Uplink
	for rows.Next() {
		var value model.Uplink
		var created, updated int64
		var admin int
		if err := rows.Scan(&value.ID, &value.Fingerprint, &value.Identity, &value.TokenCredentialID,
			&value.AuthActor, &admin, &created, &updated); err != nil {
			return nil, fmt.Errorf("scan uplink: %w", err)
		}
		value.Admin = admin != 0
		value.CreatedAt, value.UpdatedAt = fromUnixNano(created), fromUnixNano(updated)
		matches = append(matches, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("look up uplink: %w", err)
	}
	return matches, nil
}

// ListUplinkStatuses returns every registered uplink with the usage state of
// its bound token credential, ordered by identity for stable listings.
func (s *Store) ListUplinkStatuses(ctx context.Context) ([]UplinkStatus, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT u.id, u.fingerprint, u.identity, u.token_credential_id, u.auth_actor, u.admin, u.created_at, u.updated_at,
       COALESCE(t.last_used_at, 0)
FROM uplinks u LEFT JOIN token_credentials t ON t.id = u.token_credential_id
ORDER BY u.identity, u.fingerprint`)
	if err != nil {
		return nil, fmt.Errorf("list uplinks: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var values []UplinkStatus
	for rows.Next() {
		var value UplinkStatus
		var created, updated, lastUsed int64
		var admin int
		if err := rows.Scan(&value.ID, &value.Fingerprint, &value.Identity, &value.TokenCredentialID,
			&value.AuthActor, &admin, &created, &updated, &lastUsed); err != nil {
			return nil, fmt.Errorf("scan uplink: %w", err)
		}
		value.Admin = admin != 0
		value.CreatedAt, value.UpdatedAt = fromUnixNano(created), fromUnixNano(updated)
		if lastUsed > 0 {
			value.LastUsedAt = fromUnixNano(lastUsed)
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list uplinks: %w", err)
	}
	return values, nil
}

// RemoveUplink atomically deletes one uplink, revokes its bound token
// credential so the bearer token is immediately invalid, and records the audit
// action. fingerprint is optional and disambiguates when several uplinks share
// an identity.
func (s *Store) RemoveUplink(ctx context.Context, actor, identity, fingerprint string) (model.Uplink, error) {
	if strings.TrimSpace(actor) == "" || strings.TrimSpace(identity) == "" {
		return model.Uplink{}, fmt.Errorf("%w: actor and uplink identity are required", ErrInvalid)
	}
	now := unixNano(s.now())
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return model.Uplink{}, fmt.Errorf("begin uplink removal: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	matches, err := matchUplinksByIdentity(ctx, tx, identity, fingerprint)
	if err != nil {
		return model.Uplink{}, err
	}
	if len(matches) == 0 {
		return model.Uplink{}, fmt.Errorf("%w: uplink %s", ErrNotFound, identity)
	}
	if len(matches) > 1 {
		fingerprints := make([]string, 0, len(matches))
		for _, match := range matches {
			fingerprints = append(fingerprints, match.Fingerprint)
		}
		return model.Uplink{}, fmt.Errorf("%w: identity %s has %d uplinks (%s); pass --fingerprint",
			ErrAmbiguous, identity, len(matches), strings.Join(fingerprints, ", "))
	}
	value := matches[0]
	if _, err := tx.ExecContext(ctx, `DELETE FROM uplinks WHERE id = ?`, value.ID); err != nil {
		return model.Uplink{}, fmt.Errorf("remove uplink: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE token_credentials SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`, now, value.TokenCredentialID); err != nil {
		return model.Uplink{}, fmt.Errorf("revoke uplink token credential: %w", err)
	}
	details, err := json.Marshal(map[string]string{"identity": value.Identity, "fingerprint": value.Fingerprint})
	if err != nil {
		return model.Uplink{}, fmt.Errorf("encode uplink removal audit: %w", err)
	}
	if _, err := appendAuditAction(ctx, tx, model.AuditActionSpec{
		Actor: actor, Action: "uplink.remove", ResourceType: "uplink",
		ResourceID: fmt.Sprint(value.ID), Details: string(details),
	}, now); err != nil {
		return model.Uplink{}, fmt.Errorf("audit uplink removal: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return model.Uplink{}, fmt.Errorf("commit uplink removal: %w", err)
	}
	return value, nil
}

// RegisterUplink atomically binds one pending token credential to a public-key
// identity, revokes any displaced credential, and records who performed the
// registration. Token activation occurs only after this method commits.
func (s *Store) RegisterUplink(ctx context.Context, actor string, spec model.UplinkSpec) (model.Uplink, error) {
	if strings.TrimSpace(actor) == "" || strings.TrimSpace(spec.Fingerprint) == "" || strings.TrimSpace(spec.Identity) == "" ||
		strings.TrimSpace(spec.TokenCredentialID) == "" {
		return model.Uplink{}, fmt.Errorf("%w: actor and uplink fields are required", ErrInvalid)
	}
	adminInt := 0
	if spec.Admin {
		adminInt = 1
	}
	now := unixNano(s.now())
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return model.Uplink{}, fmt.Errorf("begin uplink registration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var displacedCredentialID string
	_ = tx.QueryRowContext(ctx, `SELECT token_credential_id FROM uplinks WHERE fingerprint = ?`,
		spec.Fingerprint).Scan(&displacedCredentialID)
	row := tx.QueryRowContext(ctx, `
INSERT INTO uplinks(fingerprint, identity, token_credential_id, auth_actor, admin, created_at, updated_at)
VALUES(?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(fingerprint) DO UPDATE SET
    identity = excluded.identity,
    token_credential_id = excluded.token_credential_id,
    auth_actor = excluded.auth_actor,
    admin = excluded.admin,
    updated_at = excluded.updated_at
RETURNING id, fingerprint, identity, token_credential_id, auth_actor, admin, created_at, updated_at`,
		spec.Fingerprint, spec.Identity, spec.TokenCredentialID, actor, adminInt, now, now)
	var value model.Uplink
	var created, updated int64
	var scannedAdmin int
	if err := row.Scan(&value.ID, &value.Fingerprint, &value.Identity, &value.TokenCredentialID, &value.AuthActor, &scannedAdmin, &created, &updated); err != nil {
		return model.Uplink{}, fmt.Errorf("register uplink: %w", err)
	}
	value.Admin = scannedAdmin != 0
	if displacedCredentialID != "" && displacedCredentialID != spec.TokenCredentialID {
		if _, err := tx.ExecContext(ctx, `
UPDATE token_credentials SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`, now, displacedCredentialID); err != nil {
			return model.Uplink{}, fmt.Errorf("revoke displaced credential: %w", err)
		}
	}
	auditDetails := map[string]any{
		"fingerprint": spec.Fingerprint,
		"identity":    spec.Identity,
		"admin":       spec.Admin,
	}
	if displacedCredentialID != "" && displacedCredentialID != spec.TokenCredentialID {
		auditDetails["displaced_credential"] = displacedCredentialID
	}
	details, err := json.Marshal(auditDetails)
	if err != nil {
		return model.Uplink{}, fmt.Errorf("encode uplink audit: %w", err)
	}
	if _, err := appendAuditAction(ctx, tx, model.AuditActionSpec{
		Actor: actor, Action: "uplink.register", ResourceType: "uplink",
		ResourceID: fmt.Sprint(value.ID), Details: string(details),
	}, now); err != nil {
		return model.Uplink{}, fmt.Errorf("audit uplink registration: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return model.Uplink{}, fmt.Errorf("commit uplink registration: %w", err)
	}
	value.CreatedAt, value.UpdatedAt = fromUnixNano(created), fromUnixNano(updated)
	return value, nil
}
