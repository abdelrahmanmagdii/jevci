package main

import (
	"fmt"

	"example.com/fixture/api"
	"example.com/fixture/core"
	"example.com/fixture/web"
)

func main() {
	fmt.Println(web.Page(api.New(core.NewStore()), "k"))
}
