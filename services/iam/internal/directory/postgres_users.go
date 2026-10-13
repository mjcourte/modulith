package directory

// Postgres's UserStore methods (plumbing in postgres.go).

import (
	"context"
	"fmt"

	"gyoza/services/iam/internal/db"
)

func (p *Postgres) UpsertUser(ctx context.Context, id Identity) (User, bool, error) {
	id, err := id.normalize()
	if err != nil {
		return User{}, false, err
	}
	var u User
	var created bool
	err = p.inTx(ctx, func(q *db.Queries) error {
		row, err := q.GetUserForUpdate(ctx, id.UserID)
		if noRows(err) {
			t := now()
			n, err := q.InsertUser(ctx, db.InsertUserParams{
				ID: id.UserID, Email: id.Email, EmailVerified: id.EmailVerified, Name: id.Name,
				ProviderAvatarUrl: id.ProviderAvatarURL, CreatedAt: t,
			})
			if err != nil {
				return err
			}
			if n == 1 {
				u = User{
					ID: id.UserID, Email: id.Email, EmailVerified: id.EmailVerified, Name: id.Name,
					ProviderAvatarURL: id.ProviderAvatarURL, CreatedAt: t, UpdatedAt: t,
				}
				created = true
				return nil
			}
			// A concurrent first sign-in inserted them: update that row instead.
			row, err = q.GetUserForUpdate(ctx, id.UserID)
		}
		if err != nil {
			return err
		}
		existing := userFromRow(row)
		if existing.DeletingAt != nil {
			return fmt.Errorf("%w: account being deleted", ErrUserNotFound)
		}
		// Only a real change moves UpdatedAt (every sign-in calls this).
		u = existing
		u.Email, u.EmailVerified, u.Name, u.ProviderAvatarURL = id.Email, id.EmailVerified, id.Name, id.ProviderAvatarURL
		if u == existing {
			return nil
		}
		u.UpdatedAt = now()
		return q.UpdateUserProfile(ctx, db.UpdateUserProfileParams{
			ID: u.ID, Email: u.Email, EmailVerified: u.EmailVerified, Name: u.Name,
			ProviderAvatarUrl: u.ProviderAvatarURL, UpdatedAt: u.UpdatedAt,
		})
	})
	if violates(err, "users_email_key") {
		return User{}, false, ErrEmailTaken
	}
	if err != nil {
		return User{}, false, err
	}
	return u, created, nil
}

func (p *Postgres) GetUser(ctx context.Context, id string) (User, error) {
	row, err := p.q.GetUser(ctx, id)
	if noRows(err) {
		return User{}, ErrUserNotFound
	}
	if err != nil {
		return User{}, err
	}
	return userFromRow(row), nil
}

func (p *Postgres) GetUserByEmail(ctx context.Context, email string) (User, error) {
	row, err := p.q.GetUserByEmail(ctx, emailKey(email))
	if noRows(err) {
		return User{}, ErrUserNotFound
	}
	if err != nil {
		return User{}, err
	}
	return userFromRow(row), nil
}
