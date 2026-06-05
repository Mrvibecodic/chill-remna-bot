package app

import "testing"

func TestStripHTMLTags(t *testing.T) {
	cases := map[string]string{
		"<b>Hi</b> &amp; bye":               "Hi & bye",
		"plain text":                        "plain text",
		"a < b and c > d":                   "a  d",
		"<a href=\"x\">link</a> &lt;ok&gt;": "link <ok>",
	}
	for in, want := range cases {
		if got := stripHTMLTags(in); got != want {
			t.Errorf("stripHTMLTags(%q) = %q, want %q", in, got, want)
		}
	}
}
