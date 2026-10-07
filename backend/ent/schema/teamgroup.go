package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"

	"github.com/63747756/jingjiaagent/backend/pkg/entx"
)

// TeamGroup holds the schema definition for the TeamGroup entity.
type TeamGroup struct {
	ent.Schema
}

func (TeamGroup) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Table("team_groups"),
		entx.NewCursor(entx.CursorKindCreatedAt),
	}
}

func (TeamGroup) Mixin() []ent.Mixin {
	return []ent.Mixin{
		entx.SoftDeleteMixin2{},
	}
}

// Fields of the TeamGroup.
func (TeamGroup) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Unique(),
		field.UUID("team_id", uuid.UUID{}),
		field.String("name").SchemaType(map[string]string{dialect.Postgres: "text"}),
		field.String("source").Default("manual"),
		field.UUID("directory_id", uuid.UUID{}).Optional().Nillable(),
		field.String("external_id").Optional(),
		field.String("external_dn").Optional(),
		field.String("ou_path").Optional(),
		field.Time("last_synced_at").Optional().Nillable(),
		field.Time("created_at").Default(time.Now),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (TeamGroup) Indexes() []ent.Index {
	return []ent.Index{index.Fields("team_id", "directory_id", "external_id").Unique().Annotations(entsql.IndexWhere("source = 'ad_ou' AND deleted_at IS NULL"))}
}

// Edges of the TeamGroup.
func (TeamGroup) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("members", User.Type).Ref("groups").Through("team_group_members", TeamGroupMember.Type),
		edge.From("team", Team.Type).Ref("groups").Field("team_id").Unique().Required(),
		edge.To("models", Model.Type).Through("team_group_models", TeamGroupModel.Type),
		edge.To("images", Image.Type).Through("team_group_images", TeamGroupImage.Type),
		edge.To("hosts", Host.Type).Through("team_group_hosts", TeamGroupHost.Type),
		edge.To("mcp_upstreams", MCPUpstream.Type).Through("team_group_mcp_upstreams", TeamGroupMCPUpstream.Type),
	}
}
