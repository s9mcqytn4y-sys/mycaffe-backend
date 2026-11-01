package service

import "testing"

func TestDeriveKDSStatus(t *testing.T) {
	for _, tc := range []struct {
		in   []string
		want string
	}{
		{nil, ""},
		{[]string{KDSCancelled}, ""},
		{[]string{KDSQueued, KDSQueued}, KDSQueued},
		{[]string{KDSQueued, KDSPreparing}, KDSPreparing},
		{[]string{KDSQueued, KDSReady}, KDSPreparing}, // one station done, the other not started: work in progress
		{[]string{KDSReady, KDSServed}, KDSReady},
		{[]string{KDSServed, KDSServed}, KDSServed},
		{[]string{KDSServed, KDSCancelled}, KDSServed},
	} {
		if got := deriveKDSStatus(tc.in); got != tc.want {
			t.Errorf("deriveKDSStatus(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
