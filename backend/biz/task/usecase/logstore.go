package usecase

import "github.com/63747756/jingjiaagent/backend/consts"

func normalizeTaskLogStore(store *consts.LogStore) string {
	if store == nil || *store == "" {
		return string(consts.LogStoreLoki)
	}
	return string(*store)
}
