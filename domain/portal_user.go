package domain

import (
	"time"

	"github.com/google/uuid"
)

// PortalUserRole represents the role of a portal user within a tenant.
type PortalUserRole string

const (
	PortalUserRoleOwner  PortalUserRole = "OWNER"
	PortalUserRoleAdmin  PortalUserRole = "ADMIN"
	PortalUserRoleMember PortalUserRole = "MEMBER"
)

// PortalUser represents a self-service portal login for a tenant.
type PortalUser struct {
	ID                  uuid.UUID
	TenantID            uuid.UUID
	Email               string
	PasswordHash        string
	DisplayName         string
	Role                PortalUserRole
	EmailVerified       bool
	EmailTokenHash      string
	EmailTokenExpiresAt *time.Time
	LastLoginAt         *time.Time
	CreatedAt           time.Time
	UpdatedAt           time.Time
}
