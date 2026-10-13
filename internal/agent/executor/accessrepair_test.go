package executor

import (
	"context"
	"io/fs"
	"testing"
)

func TestWithAccessRepairOnlyStandardFolders(t *testing.T) {
	SetNetboxPaths("/srv/netbox/netbox/netbox/configuration.py", "/srv/netbox/netbox/media")
	calls := 0
	err := withAccessRepair(context.Background(), "/srv/extra", nil, func() error { calls++; return fs.ErrPermission })
	if err != fs.ErrPermission || calls != 1 {
		t.Errorf("extra folder: err %v after %d calls, want the original error after 1", err, calls)
	}
	calls, tried := 0, true
	err = withAccessRepair(context.Background(), "/srv/netbox/netbox/media", &tried, func() error { calls++; return fs.ErrPermission })
	if err != fs.ErrPermission || calls != 1 || len(TakeRepaired()) != 0 {
		t.Errorf("already tried: err %v after %d calls, want no second repair", err, calls)
	}
}
