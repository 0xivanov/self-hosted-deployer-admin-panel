// Runs one disposable, networkless Linux VM. This is a lifecycle primitive,
// not a build-success oracle. Each operation needs its own private disk/EFI copy.
import Foundation
import Virtualization
import Darwin

struct LauncherError: Error { let message: String }
func fail(_ message: String) -> LauncherError { LauncherError(message: message) }

// Guest console bytes are untrusted diagnostics, never controller instructions
// or build evidence. Drain continuously but retain at most one MiB on the host.
final class ConsoleCapture {
    let pipe = Pipe()
    let fd: Int32
    let lock = NSLock()
    var remaining = 1024 * 1024
    init(directory: String) throws {
        fd = open(directory + "/console.log", O_CREAT | O_EXCL | O_WRONLY | O_NOFOLLOW, 0o600)
        guard fd >= 0 else { throw fail("Private console log unavailable") }
        pipe.fileHandleForReading.readabilityHandler = { [weak self] handle in
            let data = handle.availableData
            guard let self = self, !data.isEmpty else { return }
            self.lock.lock()
            defer { self.lock.unlock() }
            let kept = data.prefix(self.remaining)
            kept.withUnsafeBytes { bytes in
                var offset = 0
                while offset < bytes.count {
                    let n = Darwin.write(self.fd, bytes.baseAddress!.advanced(by: offset), bytes.count - offset)
                    if n < 0 && errno == EINTR { continue }
                    if n <= 0 { self.remaining = 0; return }
                    offset += n
                    self.remaining -= n
                }
            }
        }
    }
    deinit { pipe.fileHandleForReading.readabilityHandler = nil; close(fd) }
}

func checkedPath(_ path: String, directory: Bool) throws {
    var info = stat()
    guard lstat(path, &info) == 0,
          info.st_uid == getuid(), info.st_mode & 0o077 == 0,
          info.st_mode & S_IFMT == (directory ? S_IFDIR : S_IFREG),
          directory || info.st_nlink == 1 else { throw fail("Private owned operation files required") }
}

// Exclusive creation is the persistent one-shot start fence. Never delete these
// records to retry a VM; a new build needs a new execution and fresh disk copies.
func record(_ directory: String, _ name: String, _ value: [String: Any]) throws {
    let raw = try JSONSerialization.data(withJSONObject: value, options: [.sortedKeys])
    let fd = open(directory + "/" + name, O_CREAT | O_EXCL | O_WRONLY | O_NOFOLLOW, 0o600)
    guard fd >= 0 else { throw fail("Operation record already exists or is unavailable") }
    defer { close(fd) }
    try raw.withUnsafeBytes { bytes in
        var offset = 0
        while offset < bytes.count {
            let n = Darwin.write(fd, bytes.baseAddress!.advanced(by: offset), bytes.count - offset)
            if n < 0 && errno == EINTR { continue }
            guard n > 0 else { throw fail("Operation record write failed") }
            offset += n
        }
    }
    guard fsync(fd) == 0 else { throw fail("Operation record sync failed") }
    let parent = open(directory, O_RDONLY | O_DIRECTORY | O_NOFOLLOW)
    guard parent >= 0 else { throw fail("Operation directory unavailable") }
    defer { close(parent) }
    guard fsync(parent) == 0 else { throw fail("Operation directory sync failed") }
}

final class Launcher: NSObject, VZVirtualMachineDelegate {
    let vm: VZVirtualMachine
    let directory: String
    let operation: String
    let deadline: Date
    let buildDisks: Bool
    let console: ConsoleCapture?
    var timer: DispatchSourceTimer?
    var signals: [DispatchSourceSignal] = []
    var stopping = false
    var stopRequested = false
    var finished = false

    init(directory: String, operation: String, seconds: Int, buildDisks: Bool) throws {
        self.directory = directory
        self.operation = operation
        self.buildDisks = buildDisks
        console = buildDisks ? try ConsoleCapture(directory: directory) : nil
        deadline = Date().addingTimeInterval(Double(seconds))
        let configuration = VZVirtualMachineConfiguration()
        configuration.platform = VZGenericPlatformConfiguration()
        configuration.cpuCount = 2
        configuration.memorySize = 2 * 1024 * 1024 * 1024
        let boot = VZEFIBootLoader()
        boot.variableStore = VZEFIVariableStore(url: URL(fileURLWithPath: directory + "/efi"))
        configuration.bootLoader = boot
        let disk = try VZDiskImageStorageDeviceAttachment(
            url: URL(fileURLWithPath: directory + "/disk"), readOnly: false,
            cachingMode: .uncached, synchronizationMode: .full)
        configuration.storageDevices = [VZVirtioBlockDeviceConfiguration(attachment: disk)]
        if buildDisks {
            // Stable IDs avoid depending on asynchronous Linux device discovery.
            let input = try VZDiskImageStorageDeviceAttachment(
                url: URL(fileURLWithPath: directory + "/input.iso"), readOnly: true)
            let output = try VZDiskImageStorageDeviceAttachment(
                url: URL(fileURLWithPath: directory + "/output.disk"), readOnly: false,
                cachingMode: .uncached, synchronizationMode: .full)
            let inputDevice = VZVirtioBlockDeviceConfiguration(attachment: input)
            inputDevice.blockDeviceIdentifier = "deployer-input"
            let outputDevice = VZVirtioBlockDeviceConfiguration(attachment: output)
            outputDevice.blockDeviceIdentifier = "deployer-output"
            configuration.storageDevices.append(inputDevice)
            configuration.storageDevices.append(outputDevice)
        }
        configuration.entropyDevices = [VZVirtioEntropyDeviceConfiguration()]
        // No NAT, host network, sockets, shared folders, clipboard or guest agent.
        configuration.networkDevices = []
        configuration.directorySharingDevices = []
        configuration.socketDevices = []
        configuration.serialPorts = []
        if let console = console {
            let serial = VZVirtioConsoleDeviceSerialPortConfiguration()
            serial.attachment = VZFileHandleSerialPortAttachment(fileHandleForReading: nil, fileHandleForWriting: console.pipe.fileHandleForWriting)
            configuration.serialPorts = [serial]
        }
        try configuration.validate()
        vm = VZVirtualMachine(configuration: configuration)
        super.init()
        vm.delegate = self
    }

    func start() throws {
        try record(directory, "attempt.json", ["operation": operation, "deadline": deadline.timeIntervalSince1970, "build_disks": buildDisks])
        for number in [SIGINT, SIGTERM] {
            signal(number, SIG_IGN)
            let source = DispatchSource.makeSignalSource(signal: number, queue: .main)
            source.setEventHandler { [weak self] in self?.requestStop() }
            source.resume()
            signals.append(source)
        }
        let clock = DispatchSource.makeTimerSource(queue: .main)
        clock.schedule(deadline: .now() + max(0, deadline.timeIntervalSinceNow))
        clock.setEventHandler { [weak self] in self?.requestStop() }
        clock.resume()
        timer = clock
        vm.start { [self] result in
            switch result {
            case .failure:
                terminate("VM start failed; preserve its operation records")
            case .success:
                do {
                    try record(directory, "running.json", ["operation": operation, "observed_at": Date().timeIntervalSince1970, "network_devices": 0, "storage_devices": buildDisks ? 3 : 1])
                } catch {
                    stopRequested = true
                }
                if stopRequested || Date() >= deadline { requestStop() }
            }
        }
    }

    func requestStop() {
        stopRequested = true
        guard !finished, !stopping else { return }
        if vm.state == .stopped { complete(); return }
        // Starting cannot be stopped yet. Its completion callback honors this
        // request, including a signal or deadline arriving during VM startup.
        guard vm.canStop else { return }
        stopping = true
        vm.stop { [self] error in
            if error != nil { terminate("VM stop failed; preserve its operation records") }
            complete()
        }
    }

    func complete() {
        guard !finished else { return }
        guard vm.state == .stopped else { terminate("VM stop remains unconfirmed") }
        finished = true
        do {
            try record(directory, "stopped.json", ["operation": operation, "observed_at": Date().timeIntervalSince1970, "state": "stopped"])
        } catch { terminate("Stopped VM record could not be persisted") }
        print("VM stopped; no build outcome has been inferred.")
        exit(0)
    }

    func guestDidStop(_ virtualMachine: VZVirtualMachine) { complete() }
    func virtualMachine(_ virtualMachine: VZVirtualMachine, didStopWithError error: Error) {
        if vm.state == .stopped { complete() }
        else { terminate("VM failed; stop state requires reconciliation") }
    }
}

func terminate(_ message: String) -> Never {
    FileHandle.standardError.write(Data((message + "\n").utf8))
    exit(1)
}

do {
    guard CommandLine.arguments.count == 4 || (CommandLine.arguments.count == 5 && CommandLine.arguments[4] == "--build-disks") else { throw fail("usage: node-vm-macos PRIVATE_OPERATION_DIRECTORY EXECUTION_ID SECONDS [--build-disks]") }
    let directory = CommandLine.arguments[1]
    let operation = CommandLine.arguments[2]
    guard let resolved = realpath(directory, nil) else { throw fail("Operation directory unavailable") }
    defer { free(resolved) }
    guard operation.count == 64, operation.allSatisfy({ "0123456789abcdef".contains($0) }),
          let seconds = Int(CommandLine.arguments[3]), (1...900).contains(seconds),
          directory.hasPrefix("/"), String(cString: resolved) == directory else { throw fail("Invalid execution identity, directory or duration") }
    try checkedPath(directory, directory: true)
    try checkedPath(directory + "/disk", directory: false)
    try checkedPath(directory + "/efi", directory: false)
    let buildDisks = CommandLine.arguments.count == 5
    if buildDisks {
        for (name, minimum, maximum) in [("input.iso", Int64(2048), Int64(128 * 1024 * 1024)), ("output.disk", Int64(64 * 1024 * 1024), Int64(512 * 1024 * 1024))] {
            try checkedPath(directory + "/" + name, directory: false)
            var info = stat()
            guard lstat(directory + "/" + name, &info) == 0,
                  info.st_size >= minimum, info.st_size <= maximum,
                  info.st_size % 512 == 0 else { throw fail("Invalid build disk size") }
        }
    }
    let launcher = try Launcher(directory: directory, operation: operation, seconds: seconds, buildDisks: buildDisks)
    try launcher.start()
    withExtendedLifetime(launcher) { RunLoop.main.run() }
} catch {
    terminate((error as? LauncherError)?.message ?? "VM configuration unavailable")
}
