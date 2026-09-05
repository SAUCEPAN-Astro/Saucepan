package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type resolvedTask struct {
	InternalID int
	PublicID   string
}

func resolveTaskRef(ctx context.Context, ref string) (*resolvedTask, error) {
	if ref == "" {
		return nil, errors.New("missing task id")
	}
	if parsed, err := uuid.Parse(ref); err == nil {
		return lookupTaskByPublicID(ctx, parsed.String())
	}
	if internalID, err := strconv.Atoi(ref); err == nil {
		var publicID string
		err := db.QueryRow(ctx, `
			SELECT public_id::text FROM tasks WHERE id = $1
		`, internalID).Scan(&publicID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, fmt.Errorf("task not found")
			}
			return nil, err
		}
		return &resolvedTask{InternalID: internalID, PublicID: publicID}, nil
	}
	return nil, fmt.Errorf("invalid task id")
}

func lookupTaskByPublicID(ctx context.Context, publicID string) (*resolvedTask, error) {
	var internalID int
	var pid string
	err := db.QueryRow(ctx, `
		SELECT id, public_id::text FROM tasks WHERE public_id = $1
	`, publicID).Scan(&internalID, &pid)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("task not found")
		}
		return nil, err
	}
	return &resolvedTask{InternalID: internalID, PublicID: pid}, nil
}

func taskLookupCtx(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, 5*time.Second)
}
