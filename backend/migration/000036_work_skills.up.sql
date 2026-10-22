ALTER TABLE agent_skills ADD COLUMN IF NOT EXISTS publication_state varchar(24) NOT NULL DEFAULT 'published';
ALTER TABLE agent_skills ADD CONSTRAINT agent_skills_publication_state_check CHECK(publication_state IN ('draft','trial','review','published'));
ALTER TABLE agent_skill_versions ADD COLUMN IF NOT EXISTS content_hash varchar(64) NOT NULL DEFAULT '';

CREATE TABLE work_skill_trials (
    id uuid PRIMARY KEY,
    skill_id uuid NOT NULL REFERENCES agent_skills(id),
    version_id uuid NOT NULL REFERENCES agent_skill_versions(id),
    task_id uuid NOT NULL REFERENCES work_tasks(task_id),
    user_id uuid NOT NULL REFERENCES users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(version_id,task_id)
);
CREATE TABLE work_skill_reviews (
    id uuid PRIMARY KEY,
    skill_id uuid NOT NULL REFERENCES agent_skills(id),
    version_id uuid NOT NULL REFERENCES agent_skill_versions(id),
    team_id uuid NOT NULL REFERENCES teams(id),
    submitter_id uuid NOT NULL REFERENCES users(id),
    state varchar(16) NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','approved','rejected','published')),
    reviewer_id uuid REFERENCES users(id),
    note text NOT NULL DEFAULT '',
    shared_skill_id uuid REFERENCES agent_skills(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    reviewed_at timestamptz,
    published_at timestamptz
);
CREATE UNIQUE INDEX work_skill_pending_review_idx ON work_skill_reviews(skill_id,version_id,team_id) WHERE state IN ('pending','approved');
CREATE TABLE work_skill_rollbacks (
    id uuid PRIMARY KEY,
    skill_id uuid NOT NULL REFERENCES agent_skills(id),
    from_version_id uuid NOT NULL REFERENCES agent_skill_versions(id),
    to_version_id uuid NOT NULL REFERENCES agent_skill_versions(id),
    team_id uuid NOT NULL REFERENCES teams(id),
    operator_id uuid NOT NULL REFERENCES users(id),
    created_at timestamptz NOT NULL DEFAULT now()
);
