package server

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/michaelmadell/pxe-netdeploy/internal/api"
	"github.com/michaelmadell/pxe-netdeploy/internal/store"
)

func TestListDhcpLeases(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir(), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	exp := time.Now().Add(time.Hour)
	for i, ip := range []string{"10.77.0.9", "10.77.0.10", "10.77.0.100"} {
		mac := []string{"00:15:5d:00:00:01", "00:15:5d:00:00:02", "00:15:5d:00:00:03"}[i]
		if err := st.PutLease(ctx, store.Lease{Interface: "lab0", IP: netip.MustParseAddr(ip), MAC: mac, ExpiresAt: exp}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.DeclineLease(ctx, "lab0", netip.MustParseAddr("10.77.0.11"), "00:00:00:00:00:00", exp); err != nil {
		t.Fatal(err)
	}
	reserved := "10.77.0.10"
	c, err := st.CreateClient(ctx, store.ClientInput{MAC: "00:15:5d:00:00:02", ReservedIP: &reserved})
	if err != nil {
		t.Fatal(err)
	}

	s := New("test", Deps{Store: st})
	limit := 2
	res, err := s.ListDhcpLeases(ctx, api.ListDhcpLeasesRequestObject{Params: api.ListDhcpLeasesParams{Limit: &limit}})
	if err != nil {
		t.Fatal(err)
	}
	page := res.(api.ListDhcpLeases200JSONResponse)
	if len(page.Items) != 2 || page.Items[0].Ip != "10.77.0.9" || page.Items[1].Ip != "10.77.0.10" || page.NextCursor == nil {
		t.Fatalf("page 1: %+v", page)
	}
	if it := page.Items[1]; it.ClientId == nil || *it.ClientId != c.ID || !*it.Reserved {
		t.Errorf("reserved lease: %+v", it)
	}
	res, err = s.ListDhcpLeases(ctx, api.ListDhcpLeasesRequestObject{Params: api.ListDhcpLeasesParams{Limit: &limit, Cursor: page.NextCursor}})
	if err != nil {
		t.Fatal(err)
	}
	page = res.(api.ListDhcpLeases200JSONResponse)
	if len(page.Items) != 1 || page.Items[0].Ip != "10.77.0.100" || page.NextCursor != nil {
		t.Fatalf("page 2 (declined .11 must be left out): %+v", page)
	}

	bad := "!!"
	if res, _ := s.ListDhcpLeases(ctx, api.ListDhcpLeasesRequestObject{Params: api.ListDhcpLeasesParams{Cursor: &bad}}); res == nil {
		t.Fatal("no response for a bad cursor")
	} else if _, ok := res.(api.ListDhcpLeases400ApplicationProblemPlusJSONResponse); !ok {
		t.Errorf("bad cursor: %T, want 400", res)
	}
}
