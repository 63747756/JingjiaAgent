package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"github.com/google/uuid"

	"github.com/63747756/jingjiaagent/backend/consts"
	"github.com/63747756/jingjiaagent/backend/pkg/entx"
)

// User holds the schema definition for the User entity.
type User struct {
	ent.Schema
}

func (User) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Table("users"),
	}
}

func (User) Mixin() []ent.Mixin {
	return []ent.Mixin{
		entx.SoftDeleteMixin2{},
	}
}

// Fields of the User.
func (User) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Unique(),
		field.String("name").NotEmpty().SchemaType(map[string]string{dialect.Postgres: "text"}),
		field.String("email").Optional(),
		field.String("auth_source").Default("local"),
		field.String("login_name").Optional(),
		field.String("avatar_url").Optional(),
		field.String("password").Optional(),
		field.String("role").GoType(consts.UserRole("")),
		field.String("status").GoType(consts.UserStatus("")),
		field.Bool("is_blocked").Default(false),
		field.JSON("default_configs", map[consts.DefaultConfigType]uuid.UUID{}).Optional(),
		field.Time("created_at").Default(time.Now),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

// Edges of the User.
func (User) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("identities", UserIdentity.Type),
		edge.To("audits", Audit.Type),
		edge.To("teams", Team.Type).Through("team_members", TeamMember.Type),
		edge.To("groups", TeamGroup.Type).Through("team_group_members", TeamGroupMember.Type),
		edge.To("models", Model.Type),
		edge.To("images", Image.Type),
		edge.To("hosts", Host.Type),
		edge.To("vms", VirtualMachine.Type),
		edge.To("tasks", Task.Type),
		edge.To("task_model_switches", TaskModelSwitch.Type),
		edge.To("git_identities", GitIdentity.Type),
		edge.To("projects", Project.Type),
		edge.To("project_issues", ProjectIssue.Type),
		edge.To("assigned_issues", ProjectIssue.Type),
		edge.To("project_collaborators", ProjectCollaborator.Type),
		edge.To("project_issue_comments", ProjectIssueComment.Type),
		edge.To("git_bots", GitBot.Type).Through("git_bot_users", GitBotUser.Type),
		edge.To("mcp_upstreams", MCPUpstream.Type),
	}
}
