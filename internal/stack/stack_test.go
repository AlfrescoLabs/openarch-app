package stack

import (
	"strings"
	"testing"
)

func TestSources(t *testing.T) {
	for _, c := range []struct {
		s       Sources
		profile string
		extras  string
		name    string
	}{
		{Sources{Alfresco: 1}, "alfresco", "", "1 Alfresco"},
		{Sources{Alfresco: 2}, "alfresco", "alfresco-2", "2 Alfresco"},
		{Sources{Nuxeo: 1}, "nuxeo", "", "1 Nuxeo"},
		{Sources{Alfresco: 1, Nuxeo: 1}, "full", "", "1 Alfresco, 1 Nuxeo"},
		{Sources{Alfresco: 2, Nuxeo: 1}, "full", "alfresco-2", "2 Alfresco, 1 Nuxeo"},
	} {
		if err := c.s.Valid(); err != nil {
			t.Errorf("%v: %v", c.s, err)
		}
		if c.s.Profile() != c.profile || strings.Join(c.s.Extras(), ",") != c.extras || c.s.String() != c.name {
			t.Errorf("%v: %s %v %q", c.s, c.s.Profile(), c.s.Extras(), c.s.String())
		}
		// The deprecated flags and version 1 files give the same sources back.
		if back, err := SourcesOf(c.s.Profile(), c.s.Extras()); err != nil || back != c.s {
			t.Errorf("SourcesOf(%s, %v) = %v, %v", c.s.Profile(), c.s.Extras(), back, err)
		}
	}
	for _, bad := range []Sources{{}, {Alfresco: 3}, {Nuxeo: 2}, {Alfresco: -1, Nuxeo: 1}} {
		if bad.Valid() == nil {
			t.Errorf("%+v is valid", bad)
		}
	}
	if _, err := SourcesOf("nuxeo", []string{"alfresco-2"}); err == nil {
		t.Error("nuxeo with alfresco-2 is accepted")
	}
}
