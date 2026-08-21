# gotun
Client mode:

./gotun -mode client -addr [listen_addr]:[port] -tunmode [tun|tap] - tun [tunname]

Server mode:

./gotun -mode server -addr [listen_addr]:[port] -tunmode [tun|tap] - tun [tunname]

The program create tcp tunnel over tuntap

# compile and install

apt install golang

git clone https://github.com/ne-vlezay80/gotun

make 
