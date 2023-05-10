package main

import (
	"net/url"

	"github.com/lllamnyp/minicache"
)

func main() {
	c := minicache.New()
	echo := func(p []string) []byte {
		b := make([]byte, 0)
		for i := range p {
			push, _ := url.PathUnescape(p[i])
			b = append(b, []byte(push)...)
			b = append(b, '\n')
		}
		return b
	}
	c.Register("/", echo)
	c.ListenAndServe(":8080")
}
