package tui

import "testing"

func TestFixContentLength(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "updates existing header to edited body length",
			in:   "POST /x HTTP/1.1\r\nHost: h\r\nContent-Length: 3\r\n\r\nhello",
			want: "POST /x HTTP/1.1\r\nHost: h\r\nContent-Length: 5\r\n\r\nhello",
		},
		{
			name: "adds header when missing",
			in:   "POST /x HTTP/1.1\r\nHost: h\r\n\r\nabcd",
			want: "POST /x HTTP/1.1\r\nHost: h\r\nContent-Length: 4\r\n\r\nabcd",
		},
		{
			name: "case-insensitive header match",
			in:   "POST /x HTTP/1.1\r\ncontent-length: 99\r\n\r\nhi",
			want: "POST /x HTTP/1.1\r\nContent-Length: 2\r\n\r\nhi",
		},
		{
			name: "no body boundary is left unchanged",
			in:   "GET /x HTTP/1.1\r\nHost: h\r\n",
			want: "GET /x HTTP/1.1\r\nHost: h\r\n",
		},
		{
			name: "empty body sets zero",
			in:   "POST /x HTTP/1.1\r\nContent-Length: 7\r\n\r\n",
			want: "POST /x HTTP/1.1\r\nContent-Length: 0\r\n\r\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := fixContentLength(tc.in); got != tc.want {
				t.Errorf("fixContentLength()\n got: %q\nwant: %q", got, tc.want)
			}
		})
	}
}
