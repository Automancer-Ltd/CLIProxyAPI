//go:build integration

package discovery

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
	"golang.org/x/net/ipv4"
)

// These checks intentionally use real LAN multicast sockets. Run them explicitly
// on a test host, never as part of ordinary tests on a user's desktop.
func TestAdvertiserAndBrowser_Integration(t *testing.T) {
	ifaces, _ := FilterInterfaces(nil, nil)
	spec := ServiceSpec{
		InstanceName: "CPA-LiveTest-42",
		ServiceType:  DefaultServiceType,
		Domain:       DefaultDomain,
		Port:         54321,
		Subtypes:     []string{SubtypeResponses},
		TextRecords:  mustBuildTXTRecords(t, DefaultTXTOptions()),
		Interfaces:   ifaces,
	}

	adv := NewZeroconfAdvertiser()
	startCtx, startCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer startCancel()
	if err := adv.Start(startCtx, spec); err != nil {
		t.Skipf("skipping live multicast test: %v", err)
	}
	defer func() {
		if errStop := adv.Stop(); errStop != nil {
			t.Errorf("advertiser cleanup failed: %v", errStop)
		}
	}()

	browseCtx, browseCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer browseCancel()
	browser := NewZeroconfBrowser(ifaces...)
	results, err := browser.Browse(browseCtx, DefaultServiceType, DefaultDomain)
	if err != nil {
		// Some platforms (notably windows/amd64) cannot open mDNS sockets;
		// the live browse cannot run there, only outside such environments.
		if strings.Contains(err.Error(), "open mDNS sockets") {
			t.Skipf("skipping live multicast browse: %v", err)
		}
		t.Fatalf("browse failed: %v", err)
	}

	found := false
	for _, res := range results {
		if res.InstanceName == "CPA-LiveTest-42" {
			found = true
			if res.Port != 54321 {
				t.Errorf("expected port 54321, got %d", res.Port)
			}
			if res.Product != ProductCPA {
				t.Errorf("expected product %s, got %s", ProductCPA, res.Product)
			}
			osHost := extractCleanHost() + "." + DefaultDomain
			if strings.EqualFold(res.Host, osHost) {
				t.Errorf("expected proxy-specific host instead of OS host %q, got %q", osHost, res.Host)
			}
			if res.Host != "cpa-livetest-42.local." {
				t.Errorf("expected advertised host cpa-livetest-42.local., got %q", res.Host)
			}
			break
		}
	}

	if !found {
		t.Fatalf("advertised instance CPA-LiveTest-42 was not discovered")
	}

	if errDirect := verifyDirectHostQueryAndGoodbye(t, ifaces, "cpa-livetest-42.local.", adv.Stop); errDirect != nil {
		t.Fatalf("direct host query / goodbye verification failed: %v", errDirect)
	}
}

func verifyDirectHostQueryAndGoodbye(t *testing.T, ifaces []net.Interface, hostFQDN string, stopAdv func() error) error {
	t.Helper()

	qName, errName := dnsmessage.NewName(hostFQDN)
	if errName != nil {
		return errName
	}
	builder := dnsmessage.NewBuilder(nil, dnsmessage.Header{})
	if errStartQ := builder.StartQuestions(); errStartQ != nil {
		return errStartQ
	}
	if errQ := builder.Question(dnsmessage.Question{
		Name:  qName,
		Type:  dnsmessage.TypeALL,
		Class: dnsmessage.ClassINET,
	}); errQ != nil {
		return errQ
	}
	queryBytes, errFinish := builder.Finish()
	if errFinish != nil {
		return errFinish
	}

	uConn, errListen := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if errListen != nil {
		return errListen
	}
	defer func() {
		if errClose := uConn.Close(); errClose != nil {
			t.Logf("close unicast query conn: %v", errClose)
		}
	}()
	pConn := ipv4.NewPacketConn(uConn)
	for i := range ifaces {
		_ = pConn.SetMulticastInterface(&ifaces[i])
		_, _ = pConn.WriteTo(queryBytes, nil, &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: 5353})
	}

	_ = uConn.SetReadDeadline(time.Now().Add(1500 * time.Millisecond))
	buf := make([]byte, 4096)
	gotDirectAddr := false
	for !gotDirectAddr {
		n, _, errRead := uConn.ReadFromUDP(buf)
		if errRead != nil {
			break
		}
		if hasHostAddressRecord(buf[:n], hostFQDN, false) {
			gotDirectAddr = true
		}
	}
	if !gotDirectAddr {
		return fmt.Errorf("no A/AAAA response received for direct query to %s", hostFQDN)
	}

	supersetIPs := append(extractInterfaceIPs(ifaces), "198.51.100.254")
	conflictOwned, errProbeOwned := probeHostConflictOnLAN(context.Background(), hostFQDN, ifaces, supersetIPs)
	if errProbeOwned != nil || !conflictOwned {
		return fmt.Errorf("expected probeHostConflictOnLAN to detect conflict when responder already owns %s even for subset IPs (conflict=%v, err=%v)", hostFQDN, conflictOwned, errProbeOwned)
	}
	conflictFree, errProbeFree := probeHostConflictOnLAN(context.Background(), "cpa-unowned-"+hostFQDN, ifaces, supersetIPs)
	if errProbeFree != nil || conflictFree {
		return fmt.Errorf("expected probeHostConflictOnLAN not to report conflict for unowned host (conflict=%v, err=%v)", conflictFree, errProbeFree)
	}

	mConn, errMcast := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(224, 0, 0, 0), Port: 5353})
	if errMcast != nil {
		return errMcast
	}
	defer func() {
		if errClose := mConn.Close(); errClose != nil {
			t.Logf("close multicast goodbye conn: %v", errClose)
		}
	}()
	mpConn := ipv4.NewPacketConn(mConn)
	for i := range ifaces {
		_ = mpConn.JoinGroup(&ifaces[i], &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251)})
	}

	if errStop := stopAdv(); errStop != nil {
		return fmt.Errorf("stop advertiser: %w", errStop)
	}

	_ = mConn.SetReadDeadline(time.Now().Add(1500 * time.Millisecond))
	for {
		n, _, errRead := mConn.ReadFromUDP(buf)
		if errRead != nil {
			return fmt.Errorf("no TTL=0 A/AAAA goodbye record received for %s on Stop()", hostFQDN)
		}
		if hasHostAddressRecord(buf[:n], hostFQDN, true) {
			return nil
		}
	}
}

func TestHostProbeSendFailures_Integration(t *testing.T) {
	// Probe send failure on invalid interfaces (including IPv6-only or partial interface failure) must return an error.
	if _, errV6Fail := probeHostConflictOnLAN(
		context.Background(),
		"cpa-8f3b.local.",
		[]net.Interface{{Index: -1, Name: "invalid0"}},
		[]string{"2001:db8::10"},
	); errV6Fail == nil {
		t.Fatal("expected error when IPv6-only probe cannot be sent on invalid interface")
	}
	if realIfaces, _ := FilterInterfaces(nil, nil); len(realIfaces) > 0 {
		mixedIfaces := []net.Interface{realIfaces[0], {Index: -1, Name: "invalid0"}}
		if _, errPartial := probeHostConflictOnLAN(
			context.Background(),
			"cpa-8f3b.local.",
			mixedIfaces,
			[]string{"192.0.2.10"},
		); errPartial == nil {
			t.Fatal("expected error when probe fails on one of the selected interfaces")
		}
		shortCtx, shortCancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer shortCancel()
		if _, errShort := probeHostConflictOnLAN(
			shortCtx,
			"cpa-8f3b.local.",
			realIfaces[:1],
			[]string{"192.0.2.10"},
		); errShort != context.DeadlineExceeded {
			t.Fatalf("expected context.DeadlineExceeded when deadline is shorter than full probe schedule, got %v", errShort)
		}
	}

}

func TestNewHostRecordResponder_FailsAndCleansUpWhenAllMulticastJoinsFail(t *testing.T) {
	inner := &fakeZeroconfServer{}
	resp, err := newHostRecordResponder(inner, "cpa-8f3b", DefaultDomain, []string{"192.0.2.10"}, []net.Interface{{Index: -1, Name: "invalid0"}})
	if err == nil || resp != nil {
		if resp != nil {
			resp.Shutdown()
		}
		t.Fatalf("expected error when all multicast joins fail, got resp=%v err=%v", resp, err)
	}
	if inner.shutdowns != 1 {
		t.Fatalf("inner shutdowns = %d, want 1", inner.shutdowns)
	}
}
