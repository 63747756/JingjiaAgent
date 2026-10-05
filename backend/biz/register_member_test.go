package biz

import (
	"testing"

	"github.com/samber/do"

	"github.com/63747756/jingjiaagent/backend/domain"
)

type injectedMemberManager struct{ domain.MemberManager }

func TestOpenSourceRegistrationKeepsInjectedMemberManager(t *testing.T) {
	i := do.New()
	custom := &injectedMemberManager{}
	do.ProvideValue[domain.MemberManager](i, custom)
	provideStandaloneMembers(i)
	if do.MustInvoke[domain.MemberManager](i) != custom {
		t.Fatal("standalone default replaced explicitly injected member manager")
	}
}
