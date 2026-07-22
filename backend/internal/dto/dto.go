// Package dto defines request/response payloads (the front/back contract).
package dto

// ---- Auth ----

type LoginReq struct {
	Email    string `json:"email" binding:"required"`
	Password string `json:"password" binding:"required"`
	MfaCode  string `json:"mfaCode"` // TOTP code for an MFA-enrolled user (second step)
}

type LoginResp struct {
	Token     string `json:"token"`
	ExpiresAt string `json:"expiresAt"`
	User      MeResp `json:"user"`
}

// MeResp is returned by /auth/me — user + menus + capabilities.
type MeResp struct {
	ID           int64                        `json:"id"`
	Name         string                       `json:"name"`
	Email        string                       `json:"email"`
	Initials     string                       `json:"initials"`
	RoleID       int64                        `json:"roleId"`   // primary role (display/JWT)
	RoleCode     string                       `json:"roleCode"`
	RoleName     string                       `json:"roleName"`
	Layer        string                       `json:"layer"`
	RoleIDs      []int64                      `json:"roleIds"`   // every role held (union permissions)
	RoleNames    []string                     `json:"roleNames"`
	RoleCodes    []string                     `json:"roleCodes"` // codes of all held roles (e.g. contains "admin")
	CanApprove   bool                         `json:"canApprove"`
	MfaEnabled   bool                         `json:"mfaEnabled"`
	Menus        map[string]bool              `json:"menus"`
	Capabilities map[string]map[string]string `json:"capabilities"` // cap -> env -> level
}

// ---- Risk check ----

type RiskCheckReq struct {
	ConnectionID int64  `json:"connectionId" binding:"required"`
	SQL          string `json:"sql" binding:"required"`
}

type RiskCheckResp struct {
	Risk             string `json:"risk"`   // high|mid|low
	Action           string `json:"action"` // allow|approve|deny
	RequiresApproval bool   `json:"requiresApproval"`
	MatchedRule      string `json:"matchedRule"`
	Command          string `json:"command"`
	ApprovalNo       string `json:"approvalNo,omitempty"`
	AuditID          string `json:"auditId,omitempty"`
}

// ---- Terminal exec ----

type ExecReq struct {
	ConnectionID int64  `json:"connectionId" binding:"required"`
	SQL          string `json:"sql" binding:"required"`
	Reason       string `json:"reason"`
	MfaCode      string `json:"mfaCode"`  // TOTP step-up for PROD ops when MFA is required
	Database     string `json:"database"` // selected target database within the instance
}

type ExecResp struct {
	Intercepted bool   `json:"intercepted"`
	ApprovalNo  string `json:"approvalNo,omitempty"`
	AuditID     string `json:"auditId,omitempty"`
	Risk        string `json:"risk"`
	Rule        string `json:"rule,omitempty"` // matched rule text (why it was intercepted)
	Output      string `json:"output,omitempty"`
	Rows        int    `json:"rows,omitempty"`
	Ms          int    `json:"ms,omitempty"`
	// Columns/Data carry the real result set for a read (Data capped for the
	// terminal); empty for a simulated connection, where the client synthesises a
	// preview from Rows. Truncated marks a result set larger than the display cap.
	Columns   []string   `json:"columns,omitempty"`
	Data      [][]string `json:"data,omitempty"`
	Truncated bool       `json:"truncated,omitempty"`
}

// ---- Data export ----

type ExportReq struct {
	ConnectionID int64  `json:"connectionId" binding:"required"`
	SQL          string `json:"sql" binding:"required"`
	Name         string `json:"name"`
	Database     string `json:"database"` // target database within the instance (optional)
}

// ExportResp describes an encrypted+compressed export archived on the server.
type ExportResp struct {
	File     string `json:"file"`     // absolute server path
	Filename string `json:"filename"` // basename (for the download)
	Password string `json:"password"` // AES password (shown once to the operator)
	Rows     int    `json:"rows"`
	Bytes    int64  `json:"bytes"` // encrypted archive size
}

// ---- Script scan ----

type ScriptScanReq struct {
	ConnectionID int64  `json:"connectionId"`
	Content      string `json:"content" binding:"required"`
	Filename     string `json:"filename"`
	MfaCode      string `json:"mfaCode"`
	UploadID     int64  `json:"uploadId"` // >0 = already-uploaded script; don't save again
	Database     string `json:"database"` // selected target database within the instance
}

type ScannedStmt struct {
	Index   int    `json:"index"`
	SQL     string `json:"sql"`
	Command string `json:"command"`
	Risk    string `json:"risk"` // high|mid|safe
	NoWhere bool   `json:"noWhere"`
}

type ScriptScanResp struct {
	Filename   string        `json:"filename"`
	Total      int           `json:"total"`
	High       int           `json:"high"`
	Mid        int           `json:"mid"`
	Safe       int           `json:"safe"`
	HasRisky   bool          `json:"hasRisky"`
	Statements []ScannedStmt `json:"statements"`
}

// ---- Connections ----

type ConnectionCreateReq struct {
	Name     string `json:"name" binding:"required"`
	Engine   string `json:"engine" binding:"required"`
	Host     string `json:"host" binding:"required"` // host:port
	Env      string `json:"env" binding:"required"`  // prod|staging|dev
	Policy   string `json:"policy" binding:"required"`
	Username string `json:"username"` // real-execution credentials (optional)
	Password string `json:"password"`
	Database string `json:"database"` // default schema / sqlite file
}

type ConnectionStatusReq struct {
	Status string  `json:"status"`           // online|maint (empty = toggle)
	Tags   *string `json:"tags,omitempty"`   // when present, replace the connection's tags
	Policy *string `json:"policy,omitempty"` // when present, set the gateway policy
}

// RoleTagsReq assigns the DB tags a role (user group) may access.
type RoleTagsReq struct {
	Tags []string `json:"tags"`
}

// ConnectionSchemaResp is the db→table tree of a connection. For a connection with
// real credentials it is introspected live; Error carries the reason when live
// introspection fails (empty tree otherwise) so the UI can explain the empty state.
type ConnectionSchemaResp struct {
	ConnectionID int64         `json:"connectionId"`
	Databases    []SchemaDBDTO `json:"databases"`
	Error        string        `json:"error,omitempty"`
}

type SchemaDBDTO struct {
	Name    string             `json:"name"`
	Schemas []SchemaSchemaDTO  `json:"schemas,omitempty"` // database → schema → tables (PostgreSQL)
	Tables  []SchemaTableDTO   `json:"tables"`            // database → tables (flat engines)
}

// SchemaSchemaDTO is a schema within a database and its tables.
type SchemaSchemaDTO struct {
	Name   string           `json:"name"`
	Tables []SchemaTableDTO `json:"tables"`
}

type SchemaTableDTO struct {
	Name string `json:"name"`
}

// ---- Roles ----

type RoleDetailResp struct {
	ID        int64                        `json:"id"`
	Code      string                       `json:"code"`
	Name      string                       `json:"name"`
	Layer     string                       `json:"layer"`
	Icon      string                       `json:"icon"`
	Menus     map[string]bool              `json:"menus"`
	Matrix    map[string]map[string]string `json:"matrix"` // cap -> env -> level
	Members   []MemberDTO                  `json:"members"`
	MemberIDs []int64                      `json:"memberIds"`
	Tags      []string                     `json:"tags"` // DB tags this group may access
}

type MemberDTO struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Initials string `json:"initials"`
	Dept     string `json:"dept"`
}

type RoleUpdateReq struct {
	Name            string `json:"name"`
	Description     string `json:"description"`
	DefaultConnRole string `json:"defaultConnRole"`
	CanApprove      *bool  `json:"canApprove"`
}

type MenusReq struct {
	Menus map[string]bool `json:"menus" binding:"required"`
}

type CapabilitiesReq struct {
	Matrix map[string]map[string]string `json:"matrix" binding:"required"`
}

type MemberReq struct {
	UserID int64 `json:"userId" binding:"required"`
}

// ---- Risk commands ----

type RiskCommandUpsertReq struct {
	Command string            `json:"command" binding:"required"`
	Env     map[string]string `json:"env" binding:"required"` // prod/staging/dev -> high|mid|off
}

type RiskCommandPatchReq struct {
	Env   string `json:"env" binding:"required"`
	Level string `json:"level" binding:"required"`
}

// ---- Approvals ----

type ApprovalActionReq struct {
	Comment string `json:"comment"`
}

// ---- Users ----

type UserPatchReq struct {
	Status  string  `json:"status"`
	RoleID  *int64  `json:"roleId"`  // set the single primary role (kept for compatibility)
	RoleIDs []int64 `json:"roleIds"` // replace the full role set (multi-role); first is primary
}

type InviteReq struct {
	Email  string `json:"email" binding:"required"`
	RoleID int64  `json:"roleId" binding:"required"`
}

// UserCreateReq provisions an active account from the admin console with an
// initial password and one or more roles (first is the primary role).
type UserCreateReq struct {
	Email    string  `json:"email" binding:"required"`
	Name     string  `json:"name"`
	Password string  `json:"password" binding:"required"`
	RoleIDs  []int64 `json:"roleIds" binding:"required"`
}

// AdminPasswordReq carries a new password for an admin-driven reset.
type AdminPasswordReq struct {
	Password string `json:"password" binding:"required"`
}

// ---- Settings / Webhook ----

// ---- View payloads ----

type RoleBrief struct {
	ID    int64  `json:"id"`
	Code  string `json:"code"`
	Name  string `json:"name"`
	Layer string `json:"layer"`
	Icon  string `json:"icon"`
	Count int    `json:"count"`
}

type UserView struct {
	ID            int64    `json:"id"`
	Name          string   `json:"name"`
	Email         string   `json:"email"`
	Initials      string   `json:"initials"`
	Dept          string   `json:"dept"`
	Roles         []string `json:"roles"`
	RoleIDs       []int64  `json:"roleIds"`       // every role held (for the multi-role editor)
	PrimaryRoleID int64    `json:"primaryRoleId"`
	Status        string   `json:"status"`
	MFAEnabled    bool     `json:"mfaEnabled"`
	LastActive    string   `json:"lastActive"`
}

type RiskCommandView struct {
	Command string            `json:"command"`
	Env     map[string]string `json:"env"` // prod/staging/dev -> high|mid|off
}

type WebhookConfigReq struct {
	Endpoint string `json:"endpoint"`
	Secret   string `json:"secret"` // bearer token sent as `Authorization: Bearer <secret>`; empty keeps the stored one
	Events   string `json:"events"`
	RetryMax int    `json:"retryMax"`
	Enabled  bool   `json:"enabled"`
}

// ---- MFA (TOTP) ----

// MFASetupResp is returned when a user begins enrollment: the shared secret and
// an otpauth:// URI to render as a QR code. Enrollment completes via /auth/mfa/enable.
type MFASetupResp struct {
	Secret     string `json:"secret"`
	OtpauthURI string `json:"otpauthUri"`
}

// MFACodeReq carries a 6-digit TOTP code for enable/disable.
type MFACodeReq struct {
	Code string `json:"code"`
}

// ---- Notifications ----

// NotificationsResp is the inbox payload: recent items + current unread count.
type NotificationsResp struct {
	Items  any   `json:"items"` // []model.Notification
	Unread int64 `json:"unread"`
}

// MarkReadReq marks notifications read; empty IDs marks all of the user's.
type MarkReadReq struct {
	IDs []int64 `json:"ids"`
}
