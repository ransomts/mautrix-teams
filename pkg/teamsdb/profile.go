package teamsdb

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"go.mau.fi/util/dbutil"
	"maunium.net/go/mautrix/bridgev2/networkid"
)

type Profile struct {
	BridgeID    networkid.BridgeID
	TeamsUserID string
	DisplayName string
	LastSeenTS  time.Time
}

type ProfileQuery struct {
	BridgeID networkid.BridgeID
	Database *dbutil.Database
}

func (pq *ProfileQuery) GetByTeamsUserID(ctx context.Context, teamsUserID string) (*Profile, error) {
	if pq == nil || pq.Database == nil {
		return nil, errMissingDB
	}
	teamsUserID = strings.TrimSpace(teamsUserID)
	if teamsUserID == "" {
		return nil, nil
	}
	row := pq.Database.QueryRow(ctx, `
		SELECT teams_user_id, display_name, last_seen_ts
		FROM teams_profile
		WHERE bridge_id=$1 AND teams_user_id=$2
	`, pq.BridgeID, teamsUserID)
	return pq.scan(row)
}

func (pq *ProfileQuery) Upsert(ctx context.Context, teamsUserID, displayName string, lastSeen time.Time) error {
	if pq == nil || pq.Database == nil {
		return errMissingDB
	}
	teamsUserID = strings.TrimSpace(teamsUserID)
	displayName = strings.TrimSpace(displayName)
	if teamsUserID == "" || displayName == "" {
		return nil
	}
	_, err := pq.Database.Exec(ctx, `
		INSERT INTO teams_profile (bridge_id, teams_user_id, display_name, last_seen_ts)
		VALUES ($1,$2,$3,$4)
		ON CONFLICT (bridge_id, teams_user_id) DO UPDATE SET
			display_name=excluded.display_name,
			last_seen_ts=excluded.last_seen_ts
	`, pq.BridgeID, teamsUserID, displayName, lastSeen.UTC().UnixMilli())
	return err
}

// ListTeamsUserIDs returns every known Teams user ID for this bridge, so
// presence polling can seed itself from users we have already seen (enterprise
// conversations do not carry member lists).
func (pq *ProfileQuery) ListTeamsUserIDs(ctx context.Context) ([]string, error) {
	if pq == nil || pq.Database == nil {
		return nil, errMissingDB
	}
	rows, err := pq.Database.Query(ctx, `SELECT teams_user_id FROM teams_profile WHERE bridge_id=$1`, pq.BridgeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		if strings.TrimSpace(id) != "" {
			out = append(out, id)
		}
	}
	return out, rows.Err()
}

func (pq *ProfileQuery) scan(row dbutil.Scannable) (*Profile, error) {
	if row == nil {
		return nil, nil
	}
	var teamsUserID, displayName sql.NullString
	var lastSeenMS sql.NullInt64
	err := row.Scan(&teamsUserID, &displayName, &lastSeenMS)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	p := &Profile{
		BridgeID:    pq.BridgeID,
		TeamsUserID: teamsUserID.String,
		DisplayName: displayName.String,
	}
	if lastSeenMS.Valid {
		p.LastSeenTS = time.UnixMilli(lastSeenMS.Int64).UTC()
	}
	return p, nil
}
