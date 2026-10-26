package meet

import "testing"

func TestLinkIsTheWebAppsShape(t *testing.T) {
	want := "https://meet.proton.me/join/id-6MCF0BSHBR#pwd-Synth3tic4Pw"
	if got := Link("6MCF0BSHBR", "Synth3tic4Pw"); got != want {
		t.Errorf("Link = %s, want %s", got, want)
	}
}

func TestParseLink(t *testing.T) {
	for _, tc := range []struct {
		in, name, password string
		wantErr            bool
	}{
		{in: "https://meet.proton.me/join/id-6MCF0BSHBR#pwd-Synth3tic4Pw", name: "6MCF0BSHBR", password: "Synth3tic4Pw"},
		{in: "meet.proton.me/join/id-6MCF0BSHBR#pwd-Synth3tic4Pw", name: "6MCF0BSHBR", password: "Synth3tic4Pw"},
		{in: "  https://meet.proton.me/join/id-6MCF0BSHBR#pwd-Synth3tic4Pw\n", name: "6MCF0BSHBR", password: "Synth3tic4Pw"},
		// A host name is case-insensitive, so a link typed or copied with
		// capitals still names the same meeting.
		{in: "https://Meet.Proton.ME/join/id-6MCF0BSHBR#pwd-Synth3tic4Pw", name: "6MCF0BSHBR", password: "Synth3tic4Pw"},
		{in: "6MCF0BSHBR", name: "6MCF0BSHBR"},
		{in: "https://meet.proton.me/join/id-6MCF0BSHBR", name: "6MCF0BSHBR"},
		{in: "https://meet.proton.me/join/id-6MCF0BSHBR#pwd-", wantErr: true},
		{in: "https://meet.proton.me/join/id-6MCF0BSHBR#Synth3tic4Pw", wantErr: true},
		// The web join page refuses any password that is not exactly twelve
		// characters of its alphabet (isUrlPasswordValid), so a cut-off one
		// or one with a stray character is refused here too.
		{in: "https://meet.proton.me/join/id-6MCF0BSHBR#pwd-Synt", wantErr: true},
		{in: "https://meet.proton.me/join/id-6MCF0BSHBR#pwd-Synth3tic4P-", wantErr: true},
		{in: "https://meet.proton.me/join/id-6MCF0BSHBR#pwd-Synth3tic4Pwx", wantErr: true},
		{in: "https://example.com/join/id-X#pwd-Y", wantErr: true},
		{in: "https://meet.proton.me/join/6MCF0BSHBR#pwd-Synth3tic4Pw", wantErr: true},
		{in: "https://meet.proton.me/join/id-#pwd-Synth3tic4Pw", wantErr: true},
		{in: "not a meeting", wantErr: true},
		{in: "6MCF0", wantErr: true},
		{in: "", wantErr: true},
	} {
		name, password, err := ParseLink(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ParseLink(%q) = %q, %q; want an error", tc.in, name, password)
			}
			continue
		}
		if err != nil || name != tc.name || password != tc.password {
			t.Errorf("ParseLink(%q) = %q, %q, %v; want %q, %q", tc.in, name, password, err, tc.name, tc.password)
		}
	}
}
