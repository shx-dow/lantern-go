# Static peers

Lantern finds devices through mDNS, the DHT, and relay. Sometimes that is not
enough. A WSL2 NAT, a Docker bridge, or Wi-Fi that blocks multicast all break
discovery while leaving both devices perfectly reachable.

`--peer` fixes that by naming an address directly.

## The flag

```sh
lantern daemon --peer /ip4/192.168.1.20/tcp/41002/p2p/<peer-id>
```

Each value is a full multiaddr ending in `/p2p/<peer-id>`. Pass several,
comma-separated. A peer written into the config file instead uses `peer_addrs`.

The address is kept permanently and dialed on demand. If the other side is not
up yet, the daemon retries every 20 seconds until it connects. For a device you
have paired, the configured address is also recorded in the trust store at
startup, so a later restart can reach it without the flag.

## Pin the p2p port

The p2p port defaults to a random port. A configured `--peer` goes stale on
every restart unless you pin it with `--p2p-port` on both sides.

Configuring one direction is enough, because identify teaches both sides the
return path. Configure both anyway, so either side can start first.

## WSL2 and Windows

Replace the peer IDs with the values from each side's `lantern status` output.
On the WSL side, the address is the Windows host's vEthernet adapter, which is
reachable from inside WSL.

```sh
# in WSL
lantern daemon --addr 127.0.0.1:43792 --p2p-port 41001 \
  --shared-dirs ~/Share --device-name wsl --no-lan \
  --peer /ip4/172.27.224.1/tcp/41002/p2p/<windows-peer-id>
```

```powershell
# in Windows
lantern.exe daemon --addr 127.0.0.1:43782 --p2p-port 41002 `
  --shared-dirs $HOME\Share --device-name windows --no-lan `
  --peer /ip4/172.27.236.57/tcp/41001/p2p/<wsl-peer-id>
```

Run `lantern doctor --probe-peers` on both sides afterwards. It dials each
paired device and reports the reason for any failure.