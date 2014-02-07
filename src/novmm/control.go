package main

import (
    "bufio"
    "encoding/json"
    "net/rpc"
    "net/rpc/jsonrpc"
    noguest "noguest/rpc"
    "novmm/machine"
    "novmm/platform"
    "os"
    "strings"
    "syscall"
)

func handleConn(
    conn_fd int,
    server *rpc.Server,
    client *rpc.Client) {

    control_file := os.NewFile(uintptr(conn_fd), "control")
    defer control_file.Close()

    // Read single header.
    reader := bufio.NewReader(control_file)
    header, err := reader.ReadString('\n')
    if err != nil {
        control_file.Write([]byte(err.Error()))
        return
    }

    header = strings.TrimSpace(header)

    // We read a special header before diving into RPC
    // mode. This is because for the novmrun case, we turn
    // the socket into a stream of input/output events.
    // These are simply JSON serialized versions of the
    // events for the guest RPC interface.

    if header == "NOVM RUN" {

        decoder := json.NewDecoder(reader)
        encoder := json.NewEncoder(control_file)

        var start noguest.StartCommand
        err := decoder.Decode(&start)
        if err != nil {
            // Poorly encoded command.
            encoder.Encode(err.Error())
            return
        }

        // Call start.
        result := noguest.StartResult{}
        err = client.Call("Server.Start", &start, &result)
        if err != nil {
            encoder.Encode(err.Error())
            return
        }

        // Save our pid.
        pid := result.Pid
        inputs := make(chan error)
        outputs := make(chan error)
        exitcode := make(chan int)

        // This indicates we're okay.
        encoder.Encode(nil)

        // Wait for the process to exit.
        go func() {
            wait := noguest.WaitCommand{
                Pid: pid,
            }
            var wait_result noguest.WaitResult
            err := client.Call("Server.Wait", &wait, &wait_result)
            if err != nil {
                exitcode <- 1
            } else {
                exitcode <- wait_result.Exitcode
            }
        }()

        // Read from stdout & stderr.
        go func() {
            read := noguest.ReadCommand{
                Pid: pid,
                N:   4096,
            }
            var read_result noguest.ReadResult
            for {
                err := client.Call("Server.Read", &read, &read_result)
                if err != nil {
                    inputs <- err
                    return
                }
                err = encoder.Encode(read_result.Data)
                if err != nil {
                    inputs <- err
                    return
                }
            }
        }()

        // Write to stdin.
        go func() {
            write := noguest.WriteCommand{
                Pid: pid,
            }
            var write_result noguest.WriteResult
            for {
                err := decoder.Decode(&write.Data)
                if err != nil {
                    outputs <- err
                    return
                }
                err = client.Call("Server.Write", &write, &write_result)
                if err != nil {
                    outputs <- err
                    return
                }
            }
        }()

        select {
        case <-inputs:
            // Keep going.
            break

        case <-outputs:
            // Sever the connection.
            return
        }

        select {
        case status := <-exitcode:
            // Encode the exit code.
            encoder.Encode(status)
            break

        case <-outputs:
            // Sever the connection.
            return
        }

    } else if header == "NOVM RPC" {

        // Run as JSON RPC connection.
        codec := jsonrpc.NewServerCodec(control_file)
        server.ServeCodec(codec)
    }
}

func serveControl(
    control_fd int,
    vm *platform.Vm,
    proxy machine.Proxy) {

    // Bind our rpc server.
    server := rpc.NewServer()

    // Bind our client.
    codec := jsonrpc.NewClientCodec(proxy)
    client := rpc.NewClientWithCodec(codec)

    // Accept clients.
    for {
        nfd, _, err := syscall.Accept(control_fd)
        if err == nil {
            go handleConn(nfd, server, client)
        }
    }
}