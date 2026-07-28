//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

#include <arpa/inet.h>
#include <errno.h>
#include <fcntl.h>
#include <linux/if_tun.h>
#include <net/if.h>
#include <net/route.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/ioctl.h>
#include <sys/stat.h>
#include <sys/types.h>
#include <unistd.h>

static void fail(const char *operation) {
    fprintf(stderr, "%s: %s\n", operation, strerror(errno));
    exit(1);
}

static void set_address(int socket_fd, const char *name, unsigned long request,
                        const char *address) {
    struct ifreq request_data;
    struct sockaddr_in *socket_address;

    memset(&request_data, 0, sizeof(request_data));
    strncpy(request_data.ifr_name, name, IFNAMSIZ - 1);
    socket_address = (struct sockaddr_in *)&request_data.ifr_addr;
    socket_address->sin_family = AF_INET;
    if (inet_pton(AF_INET, address, &socket_address->sin_addr) != 1) {
        errno = EINVAL;
        fail("inet_pton");
    }
    if (ioctl(socket_fd, request, &request_data) < 0) {
        fail("configure TUN address");
    }
}

int main(void) {
    const char *name = "tagtarpit0";
    int tun_fd = open("/dev/net/tun", O_RDWR);
    if (tun_fd < 0) {
        fail("open /dev/net/tun");
    }

    struct ifreq interface_request;
    memset(&interface_request, 0, sizeof(interface_request));
    interface_request.ifr_flags = IFF_TUN | IFF_NO_PI;
    strncpy(interface_request.ifr_name, name, IFNAMSIZ - 1);
    if (ioctl(tun_fd, TUNSETIFF, &interface_request) < 0) {
        fail("TUNSETIFF");
    }

    int socket_fd = socket(AF_INET, SOCK_DGRAM, 0);
    if (socket_fd < 0) {
        fail("socket");
    }
    set_address(socket_fd, name, SIOCSIFADDR, "10.255.255.2");
    set_address(socket_fd, name, SIOCSIFNETMASK, "255.255.255.255");
    set_address(socket_fd, name, SIOCSIFDSTADDR, "10.255.255.1");

    memset(&interface_request, 0, sizeof(interface_request));
    strncpy(interface_request.ifr_name, name, IFNAMSIZ - 1);
    if (ioctl(socket_fd, SIOCGIFFLAGS, &interface_request) < 0) {
        fail("SIOCGIFFLAGS");
    }
    interface_request.ifr_flags |= IFF_UP | IFF_RUNNING;
    if (ioctl(socket_fd, SIOCSIFFLAGS, &interface_request) < 0) {
        fail("SIOCSIFFLAGS");
    }

    struct rtentry route;
    struct sockaddr_in *route_address;
    memset(&route, 0, sizeof(route));
    route_address = (struct sockaddr_in *)&route.rt_dst;
    route_address->sin_family = AF_INET;
    inet_pton(AF_INET, "10.255.255.1", &route_address->sin_addr);
    route_address = (struct sockaddr_in *)&route.rt_genmask;
    route_address->sin_family = AF_INET;
    inet_pton(AF_INET, "255.255.255.255", &route_address->sin_addr);
    route.rt_flags = RTF_UP | RTF_HOST;
    route.rt_dev = (char *)name;
    if (ioctl(socket_fd, SIOCADDRT, &route) < 0 && errno != EEXIST) {
        fail("SIOCADDRT");
    }
    close(socket_fd);

    int ready_fd = open("/tmp/tag-swebench-tarpit.ready",
                        O_WRONLY | O_CREAT | O_TRUNC, 0644);
    if (ready_fd < 0) {
        fail("create ready marker");
    }
    if (write(ready_fd, "ready\n", 6) != 6) {
        fail("write ready marker");
    }
    close(ready_fd);

    for (;;) {
        pause();
    }
}
