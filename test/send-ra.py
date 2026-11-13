"""Send a test router advertisement on the acceptance suite's dummy LAN."""

import ipaddress
import socket
import struct
import sys

interface = socket.if_nametoindex(sys.argv[1])
# Advertise a default router and a documentation /64 with on-link and autonomous flags.
advertisement = struct.pack("!BBHBBHII", 134, 0, 0, 64, 0, 600, 0, 0)
advertisement += struct.pack("!BBBBIII16s", 3, 4, 64, 0xC0, 600, 300, 0,
                             ipaddress.IPv6Address("2001:db8:62::").packed)
with socket.socket(socket.AF_INET6, socket.SOCK_RAW, socket.IPPROTO_ICMPV6) as sock:
    sock.setsockopt(socket.IPPROTO_IPV6, socket.IPV6_MULTICAST_HOPS, 255)
    sock.sendto(advertisement, ("ff02::1", 0, 0, interface))
