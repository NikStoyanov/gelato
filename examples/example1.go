package main

import (
	"github.com/NikStoyanov/gelato/gelato"
)

func main() {
	var b = gelato.GelatoBuilder()
	print(b.Status())
}
