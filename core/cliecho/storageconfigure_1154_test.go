package cliecho

import "testing"

// #1154: the four routes that used to claim "no verb" now name one, and the
// configuration write echoes a line that runs, field ids and all.
func TestIssue1154_TheCapacityBackendsAndConfigureRoutesNameTheirVerbs(t *testing.T) {
	for _, tc := range []struct {
		action Action
		want   string
	}{
		{Action{Method: "GET", Route: "/system/storage"}, Binary + " storage"},
		{Action{Method: "GET", Route: "/backends"}, Binary + " medium backends"},
		{Action{Method: "PUT", Route: "/storage-mediums/{id}/configuration", Params: map[string]string{"id": "usb_disk"},
			Body: []byte(`{"backend":"local_volume","fields":[{"field":"path","value":"/mnt/usb"},{"field":"prefix","value":"retnd"}],"credentials":{}}`)},
			Binary + " medium configure usb_disk --backend local_volume --field path=/mnt/usb --field prefix=retnd"},
		{Action{Method: "POST", Route: "/storage-mediums/{id}/configuration/preflight", Params: map[string]string{"id": "usb_disk"},
			Body: []byte(`{"backend":"local_volume","fields":[{"field":"path","value":"/mnt/usb"}],"credentials":{}}`)},
			Binary + " medium configure usb_disk --backend local_volume --field path=/mnt/usb --preflight"},
		{Action{Method: "PUT", Route: "/storage-mediums/{id}/configuration", Params: map[string]string{"id": "cold"},
			Body: []byte(`{"backend":"s3","fields":[{"field":"bucket","value":"acme backups"}],"credentials":{"credentials_id":"cred_01HX"}}`)},
			Binary + " medium configure cold --backend s3 --field 'bucket=acme backups' --credentials-id cred_01HX"},
	} {
		line := Echo(tc.action)
		if line.Gap != "" || line.Shell() != tc.want {
			t.Errorf("%s %s prints %q (gap %q), want %q", tc.action.Method, tc.action.Route, line.Shell(), line.Gap, tc.want)
		}
	}
}

// A credentials command is a program and its arguments, and a placeholder
// stands in for it exactly as it does on `medium add`: the line is marked as
// not runnable as printed, and the command itself is never printed.
func TestIssue1154_ConfigureNeverPrintsACredentialsCommand(t *testing.T) {
	line := Echo(Action{Method: "PUT", Route: "/storage-mediums/{id}/configuration", Params: map[string]string{"id": "cold"},
		Body: []byte(`{"backend":"s3","fields":[{"field":"bucket","value":"b"}],"credentials":{"command":["printf","%s","AKIA:SECRET"]}}`)})
	if line.Gap != "" || !line.Placeholder {
		t.Fatalf("a credentials command echoes gap %q placeholder %v, want a placeholder line", line.Gap, line.Placeholder)
	}
	if s := line.Shell(); s == "" || containsAny(s, "AKIA", "SECRET", "printf") {
		t.Errorf("the echoed line leaks the credentials command: %q", s)
	}
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
	}
	return false
}
