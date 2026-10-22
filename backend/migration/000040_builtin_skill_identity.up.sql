CREATE TABLE work_builtin_skill_sources (
 team_id uuid NOT NULL REFERENCES teams(id), package_id text NOT NULL,
 skill_id uuid NOT NULL REFERENCES agent_skills(id),
 PRIMARY KEY(team_id,package_id), UNIQUE(skill_id)
);
