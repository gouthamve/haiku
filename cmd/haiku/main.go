package main

import (
	"fmt"
	"log"

	"github.com/gouthamve/haiku/pkg/templator"
)

func main() {
	jt, err := templator.NewJsonnetTemplator("jaeger/ops-tools1-us-east4.jaeger")
	checkErr(err)

	fmt.Println(jt.Template())
}

func checkErr(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
