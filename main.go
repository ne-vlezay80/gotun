package main

import (
    "encoding/binary"
    "flag"
    "io"
    "log"
    "net"
    "os"
    "sync"
    "syscall"
    "time"
    "unsafe"
)

// Константы из заголовочных файлов Linux (<linux/if_tun.h> и <linux/if.h>)
const (
    TUNSETIFF    = 0x400454ca
    IFF_TUN      = 0x0001
    IFF_TAP      = 0x0002
    IFF_NO_PI    = 0x1000
    IFF_PERSIST  = 0x0800 // <-- Вот главный флаг, который сохраняет интерфейс
)

// Структура для системного вызова ioctl
type ifReq struct {
    Name  [16]byte
    Flags uint16
    _     [22]byte // Выравнивание до 40 байт (размер struct ifreq в ядре)
}

var bufferPool = sync.Pool{
    New: func() interface{} {
        return make([]byte, 65537)
    },
}

type SafeConn struct {
    mu   sync.RWMutex
    conn net.Conn
}

func main() {
    mode := flag.String("mode", "client", "Режим работы: client или server")
    tunName := flag.String("tun", "tun0", "Имя TUN интерфейса")
    tunMode := flag.String("tunmode", "tun", "Режим TUN интерфейса (tun - default)")
    addr := flag.String("addr", "127.0.0.1:1080", "Адрес подключения/прослушивания")
    flag.Parse()

    // Открываем TUN вручную через syscall
    ifce, err := openPersistentTun(*tunName, *tunMode == "tap")
    if err != nil {
        log.Fatalf("Ошибка создания TUN: %v", err)
    }
    defer ifce.Close()

    state := &SafeConn{}

    // Поток отправки (TUN -> TCP)
    go func() {
        buf := bufferPool.Get().([]byte)
        defer bufferPool.Put(buf)

        for {
            n, err := ifce.Read(buf[2:])
            if err != nil {
                if err == os.ErrClosed {
                    break
                }
                continue
            }

            if n > 0 {
                state.mu.RLock()
                currentTcpConn := state.conn
                state.mu.RUnlock()

                if currentTcpConn == nil {
                    continue
                }

                binary.BigEndian.PutUint16(buf[:2], uint16(n))
                _, err := currentTcpConn.Write(buf[:2+n])
                if err != nil {
                    state.mu.Lock()
                    if state.conn == currentTcpConn {
                        state.conn = nil
                    }
                    state.mu.Unlock()
                }
            }
        }
    }()

    if *mode == "server" {
        runServer(ifce, addr, state)
        return
	}  

    if *mode == "client" {
        runClient(ifce, addr, state)
	return
    }

    log.Fatal("Неизвесный режи:", *mode)
    return
}

// --- НОВАЯ ФУНКЦИЯ: Ручное открытие TUN с флагом IFF_PERSIST ---
func openPersistentTun(name string, isTAP bool) (*os.File, error) {
    fd, err := syscall.Open("/dev/net/tun", syscall.O_RDWR, 0)
    if err != nil {
        return nil, err
    }

    var flags uint16 = IFF_NO_PI | IFF_PERSIST // Включаем NO_PI (как делает water) и PERSIST
    if isTAP {
        flags |= IFF_TAP
    } else {
        flags |= IFF_TUN
    }

    var ifr ifReq
    copy(ifr.Name[:], name)
    ifr.Flags = flags

    // Делаем системный запрос к ядру
    _, _, errno := syscall.Syscall(
        syscall.SYS_IOCTL,
        uintptr(fd),
        uintptr(TUNSETIFF),
        uintptr(unsafe.Pointer(&ifr)),
    )
    if errno != 0 {
        syscall.Close(fd)
        return nil, errno
    }

    // Возвращаем стандартный *os.File (из него можно делать Read/Write как из water.Interface)
    return os.NewFile(uintptr(fd), "/dev/net/tun"), nil
}
// ---------------------------------------------------------------

func runServer(ifce *os.File, addr *string, state *SafeConn) {
    listener, err := net.Listen("tcp", *addr)
    if err != nil {
        log.Fatalf("Ошибка запуска сервера: %v", err)
    }
    defer listener.Close()

    log.Printf("Сервер ждет соединения на %s...", *addr)

    for {
        tcpConn, err := listener.Accept()
        if err != nil {
            log.Printf("Ошибка принятия соединения: %v", err)
            time.Sleep(1 * time.Second)
            continue
        }

        log.Printf("Новый клиент подключен. %s", tcpConn.RemoteAddr())
        handleConnection(ifce, tcpConn, state)
    }
}

func runClient(ifce *os.File, addr *string, state *SafeConn) {
    for {
        log.Printf("Клиент подключается к %s...", *addr)
        tcpConn, err := net.Dial("tcp", *addr)
        if err != nil {
            log.Printf("Ошибка подключения: %v", err)
            time.Sleep(2 * time.Second)
            continue
        }

        handleConnection(ifce, tcpConn, state)
        log.Printf("Соединение разорвано. Переподключение через 1 секунду...")
        time.Sleep(1 * time.Second)
    }
}

func handleConnection(ifce *os.File, tcpConn net.Conn, state *SafeConn) {
    if tcp, ok := tcpConn.(*net.TCPConn); ok {
        _ = tcp.SetNoDelay(true)
        _ = tcp.SetReadBuffer(32 * 1024 * 1024)
        _ = tcp.SetWriteBuffer(32 * 1024 * 1024)
        _ = tcp.SetKeepAlive(true)
    }

    state.mu.Lock()
    state.conn = tcpConn
    state.mu.Unlock()

    log.Printf("Туннель запущен. Фрейминг включен.")

    packetBuf := bufferPool.Get().([]byte)
    var lengthPrefix [2]byte

    for {
        _, err := io.ReadFull(tcpConn, lengthPrefix[:])
        if err != nil {
            break
        }
        packetLen := binary.BigEndian.Uint16(lengthPrefix[:])

        if packetLen > 65535 {
            log.Printf("Предупреждение: получен слишком большой пакет (%d байт), разрыв соединения", packetLen)
            break
        }

        _, err = io.ReadFull(tcpConn, packetBuf[:packetLen])
        if err != nil {
            break
        }

        _, _ = ifce.Write(packetBuf[:packetLen])
    }

    bufferPool.Put(packetBuf)

    state.mu.Lock()
    if state.conn == tcpConn {
        state.conn = nil
    }
    state.mu.Unlock()
    
    tcpConn.Close()
}
