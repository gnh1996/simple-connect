package model

import (
	"net"
	"strconv"
)

func joinHostPort(host string, port int) string {
	return net.JoinHostPort(host, strconv.Itoa(port))
}
