// Copyright 2016 The etcd Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package e2e

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"go.etcd.io/etcd/pkg/flags"
	"go.etcd.io/etcd/pkg/testutil"
	"go.etcd.io/etcd/version"
)

func TestCtlV3Version(t *testing.T) { testCtl(t, versionTest) }

func versionTest(cx ctlCtx) {
	if err := ctlV3Version(cx); err != nil {
		cx.t.Fatalf("versionTest ctlV3Version error (%v)", err)
	}
}

func ctlV3Version(cx ctlCtx) error {
	cmdArgs := append(cx.PrefixArgs(), "version")
	return spawnWithExpect(cmdArgs, version.Version)
}

// TestCtlV3DialWithHTTPScheme ensures that client handles endpoints with HTTPS scheme.
func TestCtlV3DialWithHTTPScheme(t *testing.T) {
	testCtl(t, dialWithSchemeTest, withCfg(configClientTLS))
}

func dialWithSchemeTest(cx ctlCtx) {
	cmdArgs := append(cx.prefixArgs(cx.epc.EndpointsV3()), "put", "foo", "bar")
	if err := spawnWithExpect(cmdArgs, "OK"); err != nil {
		cx.t.Fatal(err)
	}
}

type ctlCtx struct {
	t                 *testing.T
	apiPrefix         string
	cfg               etcdProcessClusterConfig
	quotaBackendBytes int64
	corruptFunc       func(string) error
	noStrictReconfig  bool

	epc *etcdProcessCluster

	envMap map[string]struct{}

	dialTimeout time.Duration
	testTimeout time.Duration

	quorum      bool // if true, set up 3-node cluster and linearizable read
	interactive bool

	user string
	pass string

	initialCorruptCheck bool

	// for compaction
	compactPhysical bool
}

type ctlOption func(*ctlCtx)

func (cx *ctlCtx) applyOpts(opts []ctlOption) {
	for _, opt := range opts {
		opt(cx)
	}
	cx.initialCorruptCheck = true
}

func withCfg(cfg etcdProcessClusterConfig) ctlOption {
	return func(cx *ctlCtx) { cx.cfg = cfg }
}

func withDialTimeout(timeout time.Duration) ctlOption {
	return func(cx *ctlCtx) { cx.dialTimeout = timeout }
}

func withTestTimeout(timeout time.Duration) ctlOption {
	return func(cx *ctlCtx) { cx.testTimeout = timeout }
}

func withQuorum() ctlOption {
	return func(cx *ctlCtx) { cx.quorum = true }
}

func withInteractive() ctlOption {
	return func(cx *ctlCtx) { cx.interactive = true }
}

func withQuota(b int64) ctlOption {
	return func(cx *ctlCtx) { cx.quotaBackendBytes = b }
}

func withCompactPhysical() ctlOption {
	return func(cx *ctlCtx) { cx.compactPhysical = true }
}

func withInitialCorruptCheck() ctlOption {
	return func(cx *ctlCtx) { cx.initialCorruptCheck = true }
}

func withCorruptFunc(f func(string) error) ctlOption {
	return func(cx *ctlCtx) { cx.corruptFunc = f }
}

func withNoStrictReconfig() ctlOption {
	return func(cx *ctlCtx) { cx.noStrictReconfig = true }
}

func withApiPrefix(p string) ctlOption {
	return func(cx *ctlCtx) { cx.apiPrefix = p }
}

func withFlagByEnv() ctlOption {
	return func(cx *ctlCtx) { cx.envMap = make(map[string]struct{}) }
}

// This function must be called after the `withCfg`, otherwise its value
// may be overwritten by `withCfg`.
func withMaxConcurrentStreams(streams uint32) ctlOption {
	return func(cx *ctlCtx) {
		cx.cfg.MaxConcurrentStreams = streams
	}
}

func getDefaultCtlCtx(t *testing.T) ctlCtx {
	return ctlCtx{
		t:           t,
		cfg:         configAutoTLS,
		dialTimeout: 7 * time.Second,
	}
}

func testCtl(t *testing.T, testFunc func(ctlCtx), opts ...ctlOption) {
	defer testutil.AfterTest(t)

	ret := getDefaultCtlCtx(t)
	ret.applyOpts(opts)

	mustEtcdctl(t)
	if !ret.quorum {
		ret.cfg = *configStandalone(ret.cfg)
	}
	if ret.quotaBackendBytes > 0 {
		ret.cfg.quotaBackendBytes = ret.quotaBackendBytes
	}
	ret.cfg.noStrictReconfig = ret.noStrictReconfig
	if ret.initialCorruptCheck {
		ret.cfg.initialCorruptCheck = ret.initialCorruptCheck
	}

	epc, err := newEtcdProcessCluster(&ret.cfg)
	if err != nil {
		t.Fatalf("could not start etcd process cluster (%v)", err)
	}
	ret.epc = epc

	defer func() {
		if ret.envMap != nil {
			for k := range ret.envMap {
				os.Unsetenv(k)
			}
		}
		if errC := ret.epc.Close(); errC != nil {
			t.Fatalf("error closing etcd processes (%v)", errC)
		}
	}()

	donec := make(chan struct{})
	go func() {
		defer close(donec)
		testFunc(ret)
	}()

	timeout := ret.getTestTimeout()

	select {
	case <-time.After(timeout):
		testutil.FatalStack(t, fmt.Sprintf("test timed out after %v", timeout))
	case <-donec:
	}
}

func (cx *ctlCtx) getTestTimeout() time.Duration {
	timeout := cx.testTimeout
	if timeout == 0 {
		timeout = 2*cx.dialTimeout + time.Second
		if cx.dialTimeout == 0 {
			timeout = 30 * time.Second
		}
	}
	return timeout
}

func (cx *ctlCtx) prefixArgs(eps []string) []string {
	fmap := make(map[string]string)
	fmap["endpoints"] = strings.Join(eps, ",")
	fmap["dial-timeout"] = cx.dialTimeout.String()
	if cx.epc.cfg.clientTLS == clientTLS {
		if cx.epc.cfg.isClientAutoTLS {
			fmap["insecure-transport"] = "false"
			fmap["insecure-skip-tls-verify"] = "true"
		} else if cx.epc.cfg.isClientCRL {
			fmap["cacert"] = caPath
			fmap["cert"] = revokedCertPath
			fmap["key"] = revokedPrivateKeyPath
		} else {
			fmap["cacert"] = caPath
			fmap["cert"] = certPath
			fmap["key"] = privateKeyPath
		}
	}
	if cx.user != "" {
		fmap["user"] = cx.user + ":" + cx.pass
	}

	useEnv := cx.envMap != nil

	cmdArgs := []string{ctlBinPath + "3"}
	for k, v := range fmap {
		if useEnv {
			ek := flags.FlagToEnv("ETCDCTL", k)
			os.Setenv(ek, v)
			cx.envMap[ek] = struct{}{}
		} else {
			cmdArgs = append(cmdArgs, fmt.Sprintf("--%s=%s", k, v))
		}
	}
	return cmdArgs
}

// PrefixArgs prefixes etcdctl command.
// Make sure to unset environment variables after tests.
func (cx *ctlCtx) PrefixArgs() []string {
	return cx.prefixArgs(cx.epc.EndpointsV3())
}

func isGRPCTimedout(err error) bool {
	return strings.Contains(err.Error(), "grpc: timed out trying to connect")
}

func (cx *ctlCtx) memberToRemove() (ep string, memberID string, clusterID string) {
	n1 := cx.cfg.clusterSize
	if n1 < 2 {
		cx.t.Fatalf("%d-node is too small to test 'member remove'", n1)
	}

	resp, err := getMemberList(*cx)
	if err != nil {
		cx.t.Fatal(err)
	}
	if n1 != len(resp.Members) {
		cx.t.Fatalf("expected %d, got %d", n1, len(resp.Members))
	}

	ep = resp.Members[0].ClientURLs[0]
	clusterID = fmt.Sprintf("%x", resp.Header.ClusterId)
	memberID = fmt.Sprintf("%x", resp.Members[1].ID)

	return ep, memberID, clusterID
}
