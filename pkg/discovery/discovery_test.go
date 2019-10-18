// Copyright 2018 PingCAP, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// See the License for the specific language governing permissions and
// limitations under the License.

package discovery

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"

	. "github.com/onsi/gomega"
	"github.com/pingcap/kvproto/pkg/pdpb"
	"github.com/pingcap/tidb-operator/pkg/pdapi"
)

type testRefresher struct {
	getCluster   func() (Cluster, error)
	getMembersFn func() (*pdapi.MembersInfo, error)
}

func (tr testRefresher) GetCluster(_ string) (Cluster, error) {
	return tr.getCluster()
}
func (tr testRefresher) GetMembers(_ string) (*pdapi.MembersInfo, error) {
	return tr.getMembersFn()
}

func TestParseAddressHostPort(t *testing.T) {
	g := NewGomegaWithT(t)

	type testcase struct {
		name string
		url  string
		host string
		port string
	}

	tests := []testcase{
		{
			name: "domain and port",
			url:  "host:80",
			host: "host",
			port: "80",
		},
		{
			name: "ip and port",
			url:  "1.2.3.4:80",
			host: "1.2.3.4",
			port: "80",
		},
	}

	testFn := func(test *testcase, t *testing.T) {
		t.Log(test.name)
		parsed, err := ParseAddress(test.url)
		g.Expect(err).To(BeNil())
		g.Expect(parsed.Hostname()).To(Equal(test.host))
		g.Expect(parsed.Port()).To(Equal(test.port))
	}
	for i := range tests {
		testFn(&tests[i], t)
	}
}

func TestDiscoveryDiscovery(t *testing.T) {
	g := NewGomegaWithT(t)

	type testcase struct {
		name         string
		ns           string
		url          string
		clusters     map[string]*clusterInfo
		cFn          func() (Cluster, error)
		getMembersFn func() (*pdapi.MembersInfo, error)
		expectFn     func(*GomegaWithT, *tidbDiscoveryMembers, string, error)
	}
	newClusterOk := func() (Cluster, error) {
		return newCluster(), nil
	}
	testFn := func(test *testcase, t *testing.T) {
		t.Log(test.name)

		td := &tidbDiscoveryMembers{
			tidbDiscovery: tidbDiscovery{clusters: test.clusters},
			refresh: testRefresher{
				getCluster:   test.cFn,
				getMembersFn: test.getMembersFn,
			},
		}
		os.Setenv("MY_POD_NAMESPACE", test.ns)

		pdName, clusterID, parsedURL, err := ParseK8sAddress(test.url)
		if err != nil {
			test.expectFn(g, td, "", err)
			return
		}
		re, err := td.Discover(pdName, clusterID, parsedURL)
		test.expectFn(g, td, re, err)
	}
	tests := []testcase{
		{
			name:     "advertisePeerUrl is empty",
			ns:       "default",
			url:      "",
			clusters: map[string]*clusterInfo{},
			cFn:      newClusterOk,
			expectFn: func(g *GomegaWithT, td *tidbDiscoveryMembers, s string, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("advertisePeerURL format is wrong:"))
				g.Expect(len(td.clusters)).To(BeZero())
			},
		},
		{
			name:     "advertisePeerUrl is wrong",
			ns:       "default",
			url:      "demo-pd-0.demo-pd-peer.default:2380",
			clusters: map[string]*clusterInfo{},
			cFn:      newClusterOk,
			expectFn: func(g *GomegaWithT, td *tidbDiscoveryMembers, s string, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(strings.Contains(err.Error(), "advertisePeerURL format is wrong: ")).To(BeTrue())
				g.Expect(len(td.clusters)).To(BeZero())
			},
		},
		{
			name:     "namespace is wrong",
			ns:       "default1",
			url:      "demo-pd-0.demo-pd-peer.default.svc:2380",
			clusters: map[string]*clusterInfo{},
			cFn:      newClusterOk,
			expectFn: func(g *GomegaWithT, td *tidbDiscoveryMembers, s string, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(strings.Contains(err.Error(), "is not equal to discovery namespace:")).To(BeTrue())
				g.Expect(len(td.clusters)).To(BeZero())
			},
		},
		{
			name:     "failed to get tidbcluster",
			ns:       "default",
			url:      "demo-pd-0.demo-pd-peer.default.svc:2380",
			clusters: map[string]*clusterInfo{},
			cFn: func() (Cluster, error) {
				return Cluster{}, fmt.Errorf("failed to get tidbcluster")
			},
			expectFn: func(g *GomegaWithT, td *tidbDiscoveryMembers, s string, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(strings.Contains(err.Error(), "failed to get tidbcluster")).To(BeTrue())
				g.Expect(len(td.clusters)).To(BeZero())
			},
		},
		{
			name:     "failed to get members",
			ns:       "default",
			url:      "demo-pd-0.demo-pd-peer.default.svc:2380",
			clusters: map[string]*clusterInfo{},
			cFn:      newClusterOk,
			getMembersFn: func() (*pdapi.MembersInfo, error) {
				return nil, fmt.Errorf("get members failed")
			},
			expectFn: func(g *GomegaWithT, td *tidbDiscoveryMembers, s string, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(strings.Contains(err.Error(), "get members failed")).To(BeTrue())
				g.Expect(len(td.clusters)).To(Equal(1))
				g.Expect(len(td.clusters["default/demo"].peers)).To(Equal(1))
				g.Expect(td.clusters["default/demo"].peers["demo-pd-0"]).To(Not(BeNil()))
			},
		},
		{
			name: "resourceVersion changed",
			ns:   "default",
			url:  "demo-pd-0.demo-pd-peer.default.svc:2380",
			cFn:  newClusterOk,
			getMembersFn: func() (*pdapi.MembersInfo, error) {
				return nil, fmt.Errorf("getMembers failed")
			},
			clusters: map[string]*clusterInfo{
				"default/demo": {
					resourceVersion: "2",
					peers: makePeers(map[string]struct{}{
						"demo-pd-0": {},
						"demo-pd-1": {},
					}),
				},
			},
			expectFn: func(g *GomegaWithT, td *tidbDiscoveryMembers, s string, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(strings.Contains(err.Error(), "getMembers failed")).To(BeTrue())
				g.Expect(len(td.clusters)).To(Equal(1))
				g.Expect(len(td.clusters["default/demo"].peers)).To(Equal(1))
				g.Expect(td.clusters["default/demo"].peers["demo-pd-0"]).To(Not(BeNil()))
			},
		},
		{
			name:     "1 cluster, first ordinal, there are no pd members",
			ns:       "default",
			url:      "demo-pd-0.demo-pd-peer.default.svc:2380",
			clusters: map[string]*clusterInfo{},
			cFn:      newClusterOk,
			getMembersFn: func() (*pdapi.MembersInfo, error) {
				return nil, fmt.Errorf("there are no pd members")
			},
			expectFn: func(g *GomegaWithT, td *tidbDiscoveryMembers, s string, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(strings.Contains(err.Error(), "there are no pd members")).To(BeTrue())
				g.Expect(len(td.clusters)).To(Equal(1))
				g.Expect(len(td.clusters["default/demo"].peers)).To(Equal(1))
				g.Expect(td.clusters["default/demo"].peers["demo-pd-0"]).To(Not(BeNil()))
			},
		},
		{
			name: "1 cluster, second ordinal, there are no pd members",
			ns:   "default",
			url:  "demo-pd-1.demo-pd-peer.default.svc:2380",
			cFn:  newClusterOk,
			getMembersFn: func() (*pdapi.MembersInfo, error) {
				return nil, fmt.Errorf("there are no pd members 2")
			},
			clusters: map[string]*clusterInfo{
				"default/demo": {
					resourceVersion: "1",
					peers: makePeers(map[string]struct{}{
						"demo-pd-0": {},
					}),
				},
			},
			expectFn: func(g *GomegaWithT, td *tidbDiscoveryMembers, s string, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(strings.Contains(err.Error(), "there are no pd members 2")).To(BeTrue())
				g.Expect(len(td.clusters)).To(Equal(1))
				g.Expect(len(td.clusters["default/demo"].peers)).To(Equal(2))
				g.Expect(td.clusters["default/demo"].peers["demo-pd-0"]).To(Not(BeNil()))
				g.Expect(td.clusters["default/demo"].peers["demo-pd-1"]).To(Not(BeNil()))
			},
		},
		{
			name: "1 cluster, third ordinal, return the initial-cluster args",
			ns:   "default",
			url:  "demo-pd-2.demo-pd-peer.default.svc:2380",
			cFn:  newClusterOk,
			clusters: map[string]*clusterInfo{
				"default/demo": {
					resourceVersion: "1",
					peers: makePeers(map[string]struct{}{
						"demo-pd-0": {},
						"demo-pd-1": {},
					}),
				},
			},
			expectFn: func(g *GomegaWithT, td *tidbDiscoveryMembers, s string, err error) {
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(len(td.clusters)).To(Equal(1))
				g.Expect(len(td.clusters["default/demo"].peers)).To(Equal(2))
				g.Expect(td.clusters["default/demo"].peers["demo-pd-0"]).To(Not(BeNil()))
				g.Expect(td.clusters["default/demo"].peers["demo-pd-1"]).To(Not(BeNil()))
				g.Expect(s).To(Equal("--initial-cluster=demo-pd-2=http://demo-pd-2.demo-pd-peer.default.svc:2380"))
			},
		},
		{
			name: "1 cluster, the first ordinal second request, get members failed",
			ns:   "default",
			url:  "demo-pd-0.demo-pd-peer.default.svc:2380",
			cFn:  newClusterOk,
			getMembersFn: func() (*pdapi.MembersInfo, error) {
				return nil, fmt.Errorf("there are no pd members 3")
			},
			clusters: map[string]*clusterInfo{
				"default/demo": {
					resourceVersion: "1",
					peers: makePeers(map[string]struct{}{
						"demo-pd-0": {},
						"demo-pd-1": {},
					}),
				},
			},
			expectFn: func(g *GomegaWithT, td *tidbDiscoveryMembers, s string, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(strings.Contains(err.Error(), "there are no pd members 3")).To(BeTrue())
				g.Expect(len(td.clusters)).To(Equal(1))
				g.Expect(len(td.clusters["default/demo"].peers)).To(Equal(2))
				g.Expect(td.clusters["default/demo"].peers["demo-pd-0"]).To(Not(BeNil()))
				g.Expect(td.clusters["default/demo"].peers["demo-pd-1"]).To(Not(BeNil()))
			},
		},
		{
			name: "1 cluster, the first ordinal third request, get members success",
			ns:   "default",
			url:  "demo-pd-0.demo-pd-peer.default.svc:2380",
			cFn:  newClusterOk,
			getMembersFn: func() (*pdapi.MembersInfo, error) {
				return &pdapi.MembersInfo{
					Members: []*pdpb.Member{
						{
							PeerUrls: []string{"demo-pd-2.demo-pd-peer.default.svc:2380"},
						},
					},
				}, nil
			},
			clusters: map[string]*clusterInfo{
				"default/demo": {
					resourceVersion: "1",
					peers: makePeers(map[string]struct{}{
						"demo-pd-0": {},
						"demo-pd-1": {},
					}),
				},
			},
			expectFn: func(g *GomegaWithT, td *tidbDiscoveryMembers, s string, err error) {
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(len(td.clusters)).To(Equal(1))
				g.Expect(len(td.clusters["default/demo"].peers)).To(Equal(1))
				g.Expect(td.clusters["default/demo"].peers["demo-pd-1"]).To(Not(BeNil()))
				g.Expect(s).To(Equal("--join=demo-pd-2.demo-pd-peer.default.svc:2379"))
			},
		},
		{
			name: "1 cluster, the second ordinal second request, get members success",
			ns:   "default",
			url:  "demo-pd-1.demo-pd-peer.default.svc:2380",
			cFn:  newClusterOk,
			getMembersFn: func() (*pdapi.MembersInfo, error) {
				return &pdapi.MembersInfo{
					Members: []*pdpb.Member{
						{
							PeerUrls: []string{"demo-pd-0.demo-pd-peer.default.svc:2380"},
						},
						{
							PeerUrls: []string{"demo-pd-2.demo-pd-peer.default.svc:2380"},
						},
					},
				}, nil
			},
			clusters: map[string]*clusterInfo{
				"default/demo": {
					resourceVersion: "1",
					peers: makePeers(map[string]struct{}{
						"demo-pd-1": {},
					}),
				},
			},
			expectFn: func(g *GomegaWithT, td *tidbDiscoveryMembers, s string, err error) {
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(len(td.clusters)).To(Equal(1))
				g.Expect(len(td.clusters["default/demo"].peers)).To(Equal(0))
				g.Expect(s).To(Equal("--join=demo-pd-0.demo-pd-peer.default.svc:2379,demo-pd-2.demo-pd-peer.default.svc:2379"))
			},
		},
		{
			name: "1 cluster, the fourth ordinal request, get members success",
			ns:   "default",
			url:  "demo-pd-3.demo-pd-peer.default.svc:2380",
			cFn: func() (Cluster, error) {
				c := newCluster()
				c.Replicas = 5
				return c, nil
			},
			getMembersFn: func() (*pdapi.MembersInfo, error) {
				return &pdapi.MembersInfo{
					Members: []*pdpb.Member{
						{
							PeerUrls: []string{"demo-pd-0.demo-pd-peer.default.svc:2380"},
						},
						{
							PeerUrls: []string{"demo-pd-1.demo-pd-peer.default.svc:2380"},
						},
						{
							PeerUrls: []string{"demo-pd-2.demo-pd-peer.default.svc:2380"},
						},
					},
				}, nil
			},
			clusters: map[string]*clusterInfo{
				"default/demo": {
					resourceVersion: "1",
					peers:           makePeers(map[string]struct{}{}),
				},
			},
			expectFn: func(g *GomegaWithT, td *tidbDiscoveryMembers, s string, err error) {
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(len(td.clusters)).To(Equal(1))
				g.Expect(len(td.clusters["default/demo"].peers)).To(Equal(0))
				g.Expect(s).To(Equal("--join=demo-pd-0.demo-pd-peer.default.svc:2379,demo-pd-1.demo-pd-peer.default.svc:2379,demo-pd-2.demo-pd-peer.default.svc:2379"))
			},
		},
		{
			name: "2 clusters, the five ordinal request, get members success",
			ns:   "default",
			url:  "demo-pd-3.demo-pd-peer.default.svc:2380",
			cFn: func() (Cluster, error) {
				c := newCluster()
				c.Replicas = 5
				return c, nil
			},
			getMembersFn: func() (*pdapi.MembersInfo, error) {
				return &pdapi.MembersInfo{
					Members: []*pdpb.Member{
						{
							PeerUrls: []string{"demo-pd-0.demo-pd-peer.default.svc:2380"},
						},
						{
							PeerUrls: []string{"demo-pd-1.demo-pd-peer.default.svc:2380"},
						},
						{
							PeerUrls: []string{"demo-pd-2.demo-pd-peer.default.svc:2380"},
						},
						{
							PeerUrls: []string{"demo-pd-3.demo-pd-peer.default.svc:2380"},
						},
					},
				}, nil
			},
			clusters: map[string]*clusterInfo{
				"default/demo": {
					resourceVersion: "1",
					peers:           makePeers(map[string]struct{}{}),
				},
				"default/demo-1": {
					peers: makePeers(map[string]struct{}{
						"demo-1-pd-0": {},
						"demo-1-pd-1": {},
						"demo-1-pd-2": {},
					}),
				},
			},
			expectFn: func(g *GomegaWithT, td *tidbDiscoveryMembers, s string, err error) {
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(len(td.clusters)).To(Equal(2))
				g.Expect(len(td.clusters["default/demo"].peers)).To(Equal(0))
				g.Expect(len(td.clusters["default/demo-1"].peers)).To(Equal(3))
				g.Expect(s).To(Equal("--join=demo-pd-0.demo-pd-peer.default.svc:2379,demo-pd-1.demo-pd-peer.default.svc:2379,demo-pd-2.demo-pd-peer.default.svc:2379,demo-pd-3.demo-pd-peer.default.svc:2379"))
			},
		},
	}
	for i := range tests {
		testFn(&tests[i], t)
	}
}

func makePeers(input map[string]struct{}) map[PDName]url.URL {
	peers := make(map[PDName]url.URL)
	for k := range input {
		peers[PDName(k)] = url.URL{}
	}
	return peers
}

func newCluster() Cluster {
	return Cluster{
		Replicas:        3,
		Scheme:          "http",
		ResourceVersion: "1",
	}
}
