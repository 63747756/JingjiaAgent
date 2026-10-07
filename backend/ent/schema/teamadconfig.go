package schema

import (
	"time"
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

type TeamADConfig struct { ent.Schema }

func (TeamADConfig) Annotations() []schema.Annotation { return []schema.Annotation{entsql.Table("team_ad_configs")} }
func (TeamADConfig) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Unique(),
		field.UUID("team_id", uuid.UUID{}),
		field.UUID("directory_id", uuid.UUID{}).Unique(),
		field.Bool("enabled").Default(false),
		field.String("display_name").Default("AD 域登录"),
		field.String("url").Optional(), field.String("base_dn").Optional(), field.String("bind_dn").Optional(),
		field.String("bind_password_ciphertext").Optional(), field.String("ca_pem").Optional(),
		field.JSON("allowed_group_dns", []string{}).Default([]string{}),
		field.Int("revision").Default(1),
		field.Time("created_at").Default(time.Now), field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}
func (TeamADConfig) Edges() []ent.Edge { return []ent.Edge{edge.To("team", Team.Type).Field("team_id").Unique().Required()} }
func (TeamADConfig) Indexes() []ent.Index { return []ent.Index{index.Fields("team_id").Unique(), index.Fields("enabled").Unique().Annotations(entsql.IndexWhere("enabled = true"))} }
