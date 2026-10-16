package store

import (
	"context"
	"time"
)

type (
	ID     string
	JSON   []byte
	Record map[string]any
	Input  map[string]any
	Patch  map[string]any
	Filter map[string]any
)

type Page struct {
	Limit  int32
	Offset int32
}

type UserRuntimeRecord struct {
	ID               string
	TeamID           string
	UserID           string
	Name             string
	APIToken         string //nolint:gosec // owner-readable Remote Runtime credential by product design
	ActivatedAt      time.Time
	PendingExpiresAt time.Time
	CreatedAt        time.Time
}

type CreateUserRuntimeInput struct {
	UserID   string
	Name     string
	APIToken string //nolint:gosec // owner-readable Remote Runtime credential by product design
}

// UserRuntimeAttach runs inside the transaction that inserts a runtime, with
// the transaction-scoped Queries, so companion rows (the runtime's user
// workspace) commit with the credential or not at all. The record lives in
// user_computers and is keyed by the user workspace it shares its id with.
type UserRuntimeAttach func(ctx context.Context, q Queries, runtime UserRuntimeRecord) error

type UserRuntimeStore interface {
	// CreateUserRuntime inserts the credential row and, when attach is not
	// nil, runs it in the same transaction. The row references the user
	// workspace it shares its id with (checked at commit), so a caller that
	// passes no attach must create that workspace itself before the
	// transaction ends.
	CreateUserRuntime(ctx context.Context, input CreateUserRuntimeInput, attach UserRuntimeAttach) (UserRuntimeRecord, error)
	GetUserRuntimeByAPIToken(ctx context.Context, apiToken string) (UserRuntimeRecord, error)
	ActivateUserRuntime(ctx context.Context, runtimeID, apiToken string) (UserRuntimeRecord, error)
	ExpirePendingUserRuntimes(ctx context.Context, userID string) error
	ListUserRuntimes(ctx context.Context, userID string) ([]UserRuntimeRecord, error)
	RevokeUserRuntime(ctx context.Context, runtimeID, userID string) error
	// BackfillUserRuntimeName sets the display name only while the row still
	// carries its creation-time default (or an empty name), so a user-chosen
	// name is never overwritten by a later handshake. Returns false when the
	// name no longer defaults (already backfilled or user-renamed).
	BackfillUserRuntimeName(ctx context.Context, runtimeID, userID, name, defaultName string) (bool, error)
}

type BotRemoteRuntimeBindingRecord struct {
	ID             string
	BotID          string
	RuntimeID      string
	IsPrimary      bool
	ToolApproval   JSON
	RuntimeName    string
	RuntimeUserID  string
	BotOwnerUserID string
	RuntimeRevoked bool
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// BotRemoteRuntimeBindingStore is the remote-mount view over
// bot_workspace_bindings: the bindings whose workspace has kind "user".
// RuntimeID is that workspace's id, which the paired runtime shares.
type BotRemoteRuntimeBindingStore interface {
	CreateOrUpdateMount(ctx context.Context, botID, runtimeID string) (BotRemoteRuntimeBindingRecord, error)
	ListMounts(ctx context.Context, botID string) ([]BotRemoteRuntimeBindingRecord, error)
	// ListGrantsByRuntimeOwner is the account-level reverse lookup: every live
	// mount held by the owner's bots. Records carry only ID/BotID/RuntimeID/
	// IsPrimary — the runtime/bot join fields stay zero.
	ListGrantsByRuntimeOwner(ctx context.Context, ownerUserID string) ([]BotRemoteRuntimeBindingRecord, error)
	GetMount(ctx context.Context, botID, targetID string) (BotRemoteRuntimeBindingRecord, error)
	GetPrimaryMount(ctx context.Context, botID string) (BotRemoteRuntimeBindingRecord, error)
	// SetPrimary makes the mount targetID the bot's primary binding. The
	// target "native" (or an empty id) makes the bot's managed workspace
	// binding primary instead and fails with db.ErrNoManagedWorkspaceBinding
	// when the bot has none.
	SetPrimary(ctx context.Context, botID, targetID string) error
	UpdateToolApproval(ctx context.Context, botID, targetID string, config JSON) error
	DeleteMount(ctx context.Context, botID, targetID string) error
}

// Workspace kinds. A managed workspace is the one the server provisions for
// a bot; a user workspace is a machine paired through Remote Runtime and
// shares the user_computers id.
const (
	WorkspaceKindManaged = "managed"
	WorkspaceKindUser    = "user"
)

// WorkspaceRecord is one computer a team knows about.
type WorkspaceRecord struct {
	ID          string
	TeamID      string
	OwnerUserID string
	Kind        string
	Name        string
	Metadata    JSON
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// CreateWorkspaceInput creates a workspace. ID is optional: user workspaces
// pass their runtime id, managed workspaces leave it empty for a fresh one.
type CreateWorkspaceInput struct {
	ID          string
	OwnerUserID string
	Kind        string
	Name        string
	Metadata    JSON
}

type WorkspaceStore interface {
	CreateWorkspace(ctx context.Context, input CreateWorkspaceInput) (WorkspaceRecord, error)
	GetWorkspace(ctx context.Context, workspaceID string) (WorkspaceRecord, error)
	ListWorkspacesByOwner(ctx context.Context, ownerUserID string) ([]WorkspaceRecord, error)
	// ListWorkspacesByBot returns every workspace the bot is bound to, in
	// binding order.
	ListWorkspacesByBot(ctx context.Context, botID string) ([]WorkspaceRecord, error)
	UpdateWorkspaceName(ctx context.Context, workspaceID, name string) (WorkspaceRecord, error)
	// DeleteWorkspace fails while a binding still references the workspace.
	DeleteWorkspace(ctx context.Context, workspaceID string) error
	// DeleteWorkspaceIfUnbound deletes the workspace only when nothing binds
	// it; it reports whether a row was removed.
	DeleteWorkspaceIfUnbound(ctx context.Context, workspaceID string) (bool, error)
}

// BotWorkspaceBindingRecord grants a bot the use of a workspace. At most one
// binding per bot is primary.
type BotWorkspaceBindingRecord struct {
	ID           string
	TeamID       string
	BotID        string
	WorkspaceID  string
	IsPrimary    bool
	ToolApproval JSON
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type BotWorkspaceBindingStore interface {
	// UpsertBotWorkspaceBinding binds the bot to the workspace; an existing
	// binding is returned unchanged apart from updated_at.
	UpsertBotWorkspaceBinding(ctx context.Context, botID, workspaceID string, primary bool) (BotWorkspaceBindingRecord, error)
	GetBotWorkspaceBinding(ctx context.Context, botID, bindingID string) (BotWorkspaceBindingRecord, error)
	ListBotWorkspaceBindingsByBot(ctx context.Context, botID string) ([]BotWorkspaceBindingRecord, error)
	ListBotWorkspaceBindingsByWorkspace(ctx context.Context, workspaceID string) ([]BotWorkspaceBindingRecord, error)
	GetPrimaryBotWorkspaceBinding(ctx context.Context, botID string) (BotWorkspaceBindingRecord, error)
	// GetBotWorkspaceBindingByWorkspace returns the bot's binding to one
	// workspace.
	GetBotWorkspaceBindingByWorkspace(ctx context.Context, botID, workspaceID string) (BotWorkspaceBindingRecord, error)
	// GetBotManagedWorkspaceBinding returns the binding behind the bot's
	// managed computer (its first managed binding).
	GetBotManagedWorkspaceBinding(ctx context.Context, botID string) (BotWorkspaceBindingRecord, error)
	// SetPrimaryBotWorkspaceBinding clears the bot's current primary and makes
	// bindingID primary in one transaction.
	SetPrimaryBotWorkspaceBinding(ctx context.Context, botID, bindingID string) error
	UpdateBotWorkspaceBindingToolApproval(ctx context.Context, botID, bindingID string, config JSON) (BotWorkspaceBindingRecord, error)
	DeleteBotWorkspaceBinding(ctx context.Context, botID, bindingID string) error
	// DeleteBotWorkspaceBindingsByWorkspace removes every bot's binding to the
	// workspace and reports how many were removed.
	DeleteBotWorkspaceBindingsByWorkspace(ctx context.Context, workspaceID string) (int64, error)
}

// BotWorkdirRecord is a named per-bot working directory: the workspace it
// lives on plus an absolute directory path. WorkspaceKind and BindingID are
// read alongside (the workspace's kind, the bot's binding to it) so the
// client-facing target address can be derived without a second lookup.
// ArchivedAt is zero while the workdir is live.
type BotWorkdirRecord struct {
	ID              string
	BotID           string
	Name            string
	WorkspaceID     string
	WorkspaceKind   string
	BindingID       string
	Path            string
	CreatedByUserID string
	ArchivedAt      time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// CreateBotWorkdirInput creates a workdir on a workspace the bot is bound to.
type CreateBotWorkdirInput struct {
	BotID           string
	Name            string
	WorkspaceID     string
	Path            string
	CreatedByUserID string
}

type BotWorkdirStore interface {
	CreateWorkdir(ctx context.Context, input CreateBotWorkdirInput) (BotWorkdirRecord, error)
	ListWorkdirs(ctx context.Context, botID string, includeArchived bool) ([]BotWorkdirRecord, error)
	// GetWorkdir returns archived rows too: sessions bound to an archived
	// workdir keep resolving their working directory. Callers that must
	// refuse archived workdirs check ArchivedAt themselves.
	GetWorkdir(ctx context.Context, botID, workdirID string) (BotWorkdirRecord, error)
	RenameWorkdir(ctx context.Context, botID, workdirID, name string) (BotWorkdirRecord, error)
	ArchiveWorkdir(ctx context.Context, botID, workdirID string) error
}

// WorkdirActivityStore coordinates directory mutations with durable run admission.
// The callback holds the same bot lock used by admission and ownership claims;
// running agents remain concurrent, but no new run can cross the idle check.
type WorkdirActivityStore interface {
	ActiveWorkdirs(context.Context, string) ([]BotWorkdirRecord, error)
	WithWorkdirMutation(context.Context, string, func([]BotWorkdirRecord) error) error
}

type AccountRecord struct {
	ID                  string
	Username            string
	Email               string
	Role                string
	DisplayName         string
	AvatarURL           string
	Timezone            string
	PasswordHash        string
	HasPasswordHash     bool
	IsActive            bool
	PrincipalActive     bool
	MembershipActive    bool
	Metadata            string
	CreatedAt           time.Time
	UpdatedAt           time.Time
	JoinedAt            time.Time
	MembershipUpdatedAt time.Time
	LastLoginAt         time.Time
	TitleModelID        string
}

type CreateUserInput struct {
	IsActive bool
	Metadata []byte
}

type CreateAccountInput struct {
	UserID       string
	Username     string
	Email        string
	PasswordHash string
	Role         string
	DisplayName  string
	AvatarURL    string
	IsActive     bool
	DataRoot     string
}

type UpdateAccountAdminInput struct {
	UserID   string
	Role     string
	IsActive *bool
}

type UpdateAccountProfileInput struct {
	UserID       string
	DisplayName  string
	AvatarURL    string
	Timezone     string
	Metadata     string
	TitleModelID string
}

type UpdateAccountPasswordInput struct {
	UserID       string
	PasswordHash string
}

type AccountStore interface {
	CountAccounts(ctx context.Context) (int64, error)
	GetByUserID(ctx context.Context, userID string) (AccountRecord, error)
	GetByIdentity(ctx context.Context, identity string) (AccountRecord, error)
	List(ctx context.Context) ([]AccountRecord, error)
	Search(ctx context.Context, query string, limit int32) ([]AccountRecord, error)
	CreateUser(ctx context.Context, input CreateUserInput) (AccountRecord, error)
	CreateAccount(ctx context.Context, input CreateAccountInput) (AccountRecord, error)
	UpdateLastLogin(ctx context.Context, accountID string) error
	UpdateAdmin(ctx context.Context, input UpdateAccountAdminInput) (AccountRecord, error)
	UpdateProfile(ctx context.Context, input UpdateAccountProfileInput) (AccountRecord, error)
	IsValidTitleModel(ctx context.Context, modelID string) (bool, error)
	UpdatePassword(ctx context.Context, input UpdateAccountPasswordInput) error
	RemoveMember(ctx context.Context, userID string) error
}

type BotStore interface {
	Create(ctx context.Context, input Input) (Record, error)
	GetByID(ctx context.Context, botID ID) (Record, error)
	ListByOwner(ctx context.Context, ownerUserID ID) ([]Record, error)
	UpdateProfile(ctx context.Context, botID ID, input Patch) (Record, error)
	UpdateOwner(ctx context.Context, botID ID, ownerUserID ID) (Record, error)
	UpdateStatus(ctx context.Context, botID ID, status string) (Record, error)
	TouchActivity(ctx context.Context, botID ID) error
	Delete(ctx context.Context, botID ID) error
}

type BotReader interface {
	GetByID(ctx context.Context, botID ID) (Record, error)
}

type ModelStore interface {
	Create(ctx context.Context, input Input) (Record, error)
	GetByID(ctx context.Context, modelID ID) (Record, error)
	GetByModelID(ctx context.Context, providerModelID string) ([]Record, error)
	List(ctx context.Context, filter Filter) ([]Record, error)
	ListByType(ctx context.Context, modelType string) ([]Record, error)
	ListByProviderID(ctx context.Context, providerID ID) ([]Record, error)
	ListEnabled(ctx context.Context, filter Filter) ([]Record, error)
	Update(ctx context.Context, modelID ID, input Patch) (Record, error)
	Delete(ctx context.Context, modelID ID) error
	Count(ctx context.Context, filter Filter) (int64, error)
	DeleteByProviderAndType(ctx context.Context, providerID ID, modelType string) error
}

type ProviderStore interface {
	Create(ctx context.Context, input Input) (Record, error)
	GetByID(ctx context.Context, providerID ID) (Record, error)
	GetByName(ctx context.Context, name string) (Record, error)
	GetByClientType(ctx context.Context, clientType string) (Record, error)
	List(ctx context.Context) ([]Record, error)
	Update(ctx context.Context, providerID ID, input Patch) (Record, error)
	Delete(ctx context.Context, providerID ID) error
	Count(ctx context.Context) (int64, error)
}

type ProviderOAuthStore interface {
	GetTokenByProvider(ctx context.Context, providerID ID) (Record, error)
	GetTokenByState(ctx context.Context, state string) (Record, error)
	UpdateOAuthState(ctx context.Context, providerID ID, state string) error
	UpsertToken(ctx context.Context, input Input) (Record, error)
	DeleteToken(ctx context.Context, providerID ID) error
}

type UserProviderOAuthStore interface {
	GetTokenByProvider(ctx context.Context, userID ID, providerID ID) (Record, error)
	GetTokenByState(ctx context.Context, state string) (Record, error)
	UpdateOAuthState(ctx context.Context, userID ID, providerID ID, state string) error
	UpsertToken(ctx context.Context, input Input) (Record, error)
	DeleteToken(ctx context.Context, userID ID, providerID ID) error
}

type ProviderCredentialsReader interface {
	GetProviderCredentials(ctx context.Context, providerID ID) (Record, error)
}

type BotSettingsStore interface {
	GetByBotID(ctx context.Context, botID ID) (Record, error)
	Upsert(ctx context.Context, botID ID, input Input) (Record, error)
	DeleteByBotID(ctx context.Context, botID ID) error
}

type SearchProviderStore interface {
	Create(ctx context.Context, input Input) (Record, error)
	GetByID(ctx context.Context, id ID) (Record, error)
	GetRawByID(ctx context.Context, id ID) (Record, error)
	List(ctx context.Context) ([]Record, error)
	ListByProvider(ctx context.Context, provider string) ([]Record, error)
	Update(ctx context.Context, id ID, input Patch) (Record, error)
	Delete(ctx context.Context, id ID) error
}

type MessageRepository interface {
	Create(ctx context.Context, input Input) (Record, error)
	CreateAsset(ctx context.Context, input Input) (Record, error)
	ListByBot(ctx context.Context, filter Filter) ([]Record, error)
	ListBySession(ctx context.Context, sessionID ID, filter Filter) ([]Record, error)
	ListLatestBySession(ctx context.Context, sessionID ID, limit int32) ([]Record, error)
	ListBefore(ctx context.Context, sessionID ID, before string, limit int32) ([]Record, error)
	Search(ctx context.Context, filter Filter) ([]Record, error)
	DeleteByBot(ctx context.Context, botID ID) error
	DeleteBySession(ctx context.Context, sessionID ID) error
	ListAssetsBatch(ctx context.Context, messageIDs []ID) ([]Record, error)
}

type SessionRepository interface {
	Create(ctx context.Context, input Input) (Record, error)
	GetByID(ctx context.Context, sessionID ID) (Record, error)
	ListByBot(ctx context.Context, botID ID) ([]Record, error)
	ListByRoute(ctx context.Context, routeID ID) ([]Record, error)
	UpdateTitle(ctx context.Context, sessionID ID, title string) (Record, error)
	UpdateMetadata(ctx context.Context, sessionID ID, metadata JSON) (Record, error)
	Touch(ctx context.Context, sessionID ID) error
	SoftDelete(ctx context.Context, sessionID ID) error
}

type PipelineSessionEventRepository interface {
	InsertIdempotent(ctx context.Context, input Input) (ID, error)
	ListBySession(ctx context.Context, sessionID ID, filter Filter) ([]Record, error)
	CountBySession(ctx context.Context, sessionID ID) (int64, error)
}

type CompactionRepository interface {
	CreateLog(ctx context.Context, input Input) (Record, error)
	GetLogByID(ctx context.Context, logID ID) (Record, error)
	ListLogsBySession(ctx context.Context, sessionID ID) ([]Record, error)
	ListLogsByBot(ctx context.Context, botID ID, page Page) ([]Record, error)
	CountLogsByBot(ctx context.Context, botID ID) (int64, error)
	ListUncompactedMessagesBySession(ctx context.Context, sessionID ID) ([]Record, error)
	MarkMessagesCompacted(ctx context.Context, messageIDs []ID, logID ID) error
	CompleteLog(ctx context.Context, logID ID, input Patch) (Record, error)
}

type ChannelConfigRepository interface {
	UpsertBotChannelConfig(ctx context.Context, input Input) (Record, error)
	DeleteBotChannelConfig(ctx context.Context, id ID) error
	UpdateBotChannelConfigDisabled(ctx context.Context, id ID, disabled bool) (Record, error)
	GetBotChannelConfig(ctx context.Context, botID ID, channelType string) (Record, error)
	ListBotChannelConfigsByType(ctx context.Context, channelType string) ([]Record, error)
	SaveMatrixSyncSinceToken(ctx context.Context, botID ID, token string) error
	UpsertUserChannelBinding(ctx context.Context, input Input) (Record, error)
	GetUserChannelBinding(ctx context.Context, userID ID, channelType string) (Record, error)
}

type ChannelIdentityRepository interface {
	Create(ctx context.Context, input Input) (Record, error)
	GetByID(ctx context.Context, id ID) (Record, error)
	UpsertByChannelSubject(ctx context.Context, input Input) (Record, error)
	Search(ctx context.Context, filter Filter) ([]Record, error)
	GetUserByID(ctx context.Context, userID ID) (Record, error)
}

type ChannelRouteRepository interface {
	Create(ctx context.Context, input Input) (Record, error)
	Find(ctx context.Context, botID ID, platform string, conversationID string, threadID string) (Record, error)
	ResolveConversation(ctx context.Context, input Input) (Record, error)
	GetByID(ctx context.Context, routeID ID) (Record, error)
	List(ctx context.Context, filter Filter) ([]Record, error)
	Delete(ctx context.Context, routeID ID) error
	UpdateReplyTarget(ctx context.Context, routeID ID, target string) (Record, error)
	UpdateMetadata(ctx context.Context, routeID ID, metadata JSON) (Record, error)
}

type BotACLStore interface {
	Evaluate(ctx context.Context, input Input) (Record, error)
	GetDefaultEffect(ctx context.Context, botID ID) (string, error)
	SetDefaultEffect(ctx context.Context, botID ID, effect string) error
	ListRules(ctx context.Context, botID ID) ([]Record, error)
	CreateRule(ctx context.Context, input Input) (Record, error)
	UpdateRule(ctx context.Context, id ID, input Patch) (Record, error)
	DeleteRule(ctx context.Context, id ID) error
}

type ObservedConversationStore interface {
	ListByChannelIdentity(ctx context.Context, identityID ID) ([]Record, error)
	ListByChannelType(ctx context.Context, channelType string) ([]Record, error)
}

type ScheduleStore interface {
	ListEnabled(ctx context.Context) ([]Record, error)
	Create(ctx context.Context, input Input) (Record, error)
	GetByID(ctx context.Context, id ID) (Record, error)
	ListByBot(ctx context.Context, botID ID) ([]Record, error)
	Update(ctx context.Context, id ID, input Patch) (Record, error)
	Delete(ctx context.Context, id ID) error
	IncrementCalls(ctx context.Context, id ID) (Record, error)
	CreateLog(ctx context.Context, input Input) (Record, error)
	CompleteLog(ctx context.Context, id ID, input Patch) (Record, error)
	ListLogsByBot(ctx context.Context, botID ID, page Page) ([]Record, error)
	ListLogsBySchedule(ctx context.Context, scheduleID ID, page Page) ([]Record, error)
	CountLogsByBot(ctx context.Context, botID ID) (int64, error)
	CountLogsBySchedule(ctx context.Context, scheduleID ID) (int64, error)
	DeleteLogsByBot(ctx context.Context, botID ID) error
}

type TokenUsageRepository interface {
	GetByDayAndType(ctx context.Context, filter Filter) ([]Record, error)
	GetByModel(ctx context.Context, filter Filter) ([]Record, error)
	ListDetail(ctx context.Context, filter Filter) ([]Record, error)
	CountDetail(ctx context.Context, filter Filter) (int64, error)
}

type MCPConnectionStore interface {
	ListByBot(ctx context.Context, botID ID) ([]Record, error)
	GetByID(ctx context.Context, id ID) (Record, error)
	Create(ctx context.Context, input Input) (Record, error)
	Update(ctx context.Context, id ID, input Patch) (Record, error)
	UpsertByName(ctx context.Context, input Input) (Record, error)
	Delete(ctx context.Context, id ID) error
	UpdateProbeResult(ctx context.Context, id ID, input Patch) error
}

type MCPOAuthStore interface {
	UpsertDiscovery(ctx context.Context, input Input) (Record, error)
	GetToken(ctx context.Context, connectionID ID) (Record, error)
	GetTokenByState(ctx context.Context, state string) (Record, error)
	UpdatePKCEState(ctx context.Context, connectionID ID, input Patch) error
	UpdateClientSecret(ctx context.Context, connectionID ID, secret string) error
	UpdateTokens(ctx context.Context, connectionID ID, input Patch) error
	UpdateConnectionAuthType(ctx context.Context, connectionID ID, authType string) error
	ClearTokens(ctx context.Context, connectionID ID) error
}

type MemoryProviderStore interface {
	Create(ctx context.Context, input Input) (Record, error)
	GetByID(ctx context.Context, id ID) (Record, error)
	GetDefault(ctx context.Context) (Record, error)
	List(ctx context.Context) ([]Record, error)
	Update(ctx context.Context, id ID, input Patch) (Record, error)
	Delete(ctx context.Context, id ID) error
}

// ManagedComputerRecord is the instance view of a managed computer: the
// container the workspace manager recorded for the workspace. The
// reconciler's intent / observation columns of the same row are read through
// botworkspace.Repository. InstanceID is empty until a container exists.
type ManagedComputerRecord struct {
	WorkspaceID    string
	TeamID         string
	Image          string
	InstanceID     string
	InstanceName   string
	InstanceStatus string
	Namespace      string
	AutoStart      bool
	ContainerPath  string
	LastStartedAt  time.Time
	LastStoppedAt  time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// ManagedComputerInstanceInput records the container behind a managed
// computer. A computer without a row yet gets one that is desired present
// and observed by Status; an existing row keeps its intent and observation.
type ManagedComputerInstanceInput struct {
	WorkspaceID   string
	InstanceID    string
	InstanceName  string
	Image         string
	Status        string
	Namespace     string
	AutoStart     bool
	ContainerPath string
}

// ManagedComputerSnapshotRecord is a snapshot of a managed computer. Version
// is nil when no version points at the snapshot.
type ManagedComputerSnapshotRecord struct {
	ID                        string
	WorkspaceID               string
	ContainerID               string
	RuntimeSnapshotName       string
	DisplayName               string
	ParentRuntimeSnapshotName string
	Snapshotter               string
	Source                    string
	Version                   *int
	CreatedAt                 time.Time
}

// ManagedComputerVersionRecord is a numbered version of a managed computer
// together with the snapshot it points at.
type ManagedComputerVersionRecord struct {
	ID                  string
	WorkspaceID         string
	ContainerID         string
	SnapshotID          string
	Version             int
	RuntimeSnapshotName string
	DisplayName         string
	CreatedAt           time.Time
}

// RecordManagedComputerVersionInput upserts the snapshot row and assigns it
// the computer's next version number in one transaction.
type RecordManagedComputerVersionInput struct {
	WorkspaceID               string
	ContainerID               string
	RuntimeSnapshotName       string
	DisplayName               string
	ParentRuntimeSnapshotName string
	Snapshotter               string
	Source                    string
}

type ManagedComputerEventInput struct {
	ID          string
	WorkspaceID string
	ContainerID string
	EventType   string
	Payload     JSON
}

// ManagedComputerResourceLimits are the desired limits of a computer; 0
// means unlimited.
type ManagedComputerResourceLimits struct {
	CPUMillicores int64
	MemoryBytes   int64
	StorageBytes  int64
}

// ManagedComputerStore is where the workspace manager keeps everything it
// records about a managed computer: the container instance, its snapshots,
// versions, lifecycle events and resource limits. Every method is keyed by
// the workspace id; callers resolve a bot through its bindings first.
// Lookups return db.ErrNotFound when the computer has no row.
type ManagedComputerStore interface {
	GetManagedComputer(ctx context.Context, workspaceID string) (ManagedComputerRecord, error)
	GetManagedComputerByInstanceID(ctx context.Context, instanceID string) (ManagedComputerRecord, error)
	// ListAutoStartManagedComputers lists the computers with a recorded
	// container that the startup reconcile checks.
	ListAutoStartManagedComputers(ctx context.Context) ([]ManagedComputerRecord, error)
	UpsertManagedComputerInstance(ctx context.Context, input ManagedComputerInstanceInput) error
	// ClearManagedComputerInstance forgets the container: the instance
	// columns are reset and the snapshots, versions and events taken on it
	// are deleted. The computer and its intent stay.
	ClearManagedComputerInstance(ctx context.Context, workspaceID string) error
	MarkManagedComputerStarted(ctx context.Context, workspaceID string) error
	MarkManagedComputerStopped(ctx context.Context, workspaceID string) error
	UpdateManagedComputerInstanceStatus(ctx context.Context, workspaceID, status string) error
	ListManagedComputerSnapshots(ctx context.Context, workspaceID string) ([]ManagedComputerSnapshotRecord, error)
	ListManagedComputerVersions(ctx context.Context, workspaceID string) ([]ManagedComputerVersionRecord, error)
	GetManagedComputerVersionSnapshotName(ctx context.Context, workspaceID string, version int) (string, error)
	RecordManagedComputerVersion(ctx context.Context, input RecordManagedComputerVersionInput) (ManagedComputerVersionRecord, error)
	InsertManagedComputerEvent(ctx context.Context, input ManagedComputerEventInput) error
	GetManagedComputerResourceLimits(ctx context.Context, workspaceID string) (ManagedComputerResourceLimits, error)
	UpsertManagedComputerResourceLimits(ctx context.Context, workspaceID string, limits ManagedComputerResourceLimits) error
}

type RegistryStore interface {
	UpsertProvider(ctx context.Context, definition Input) (Record, error)
	UpsertModel(ctx context.Context, providerID ID, definition Input) (Record, error)
}

type AudioCatalogStore interface {
	ListSpeechProviders(ctx context.Context) ([]Record, error)
	ListTranscriptionProviders(ctx context.Context) ([]Record, error)
	ListSpeechModels(ctx context.Context, filter Filter) ([]Record, error)
	ListTranscriptionModels(ctx context.Context, filter Filter) ([]Record, error)
	GetSpeechModelWithProvider(ctx context.Context, modelID ID) (Record, error)
	GetTranscriptionModelWithProvider(ctx context.Context, modelID ID) (Record, error)
	UpdateModel(ctx context.Context, modelID ID, input Patch) (Record, error)
}

type MessageSearch interface {
	SearchMessages(ctx context.Context, filter Filter) ([]Record, error)
}
