// Runs one disposable, networkless Linux VM. This is a lifecycle primitive,
// not a build-success oracle. Each operation needs its own private disk/EFI copy.
import Foundation
import Virtualization
import Darwin

struct LauncherError: Error { let message: String }
func fail(_ message: String) -> LauncherError { LauncherError(message: message) }

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
    var timer: DispatchSourceTimer?
    var signals: [DispatchSourceSignal] = []
    var stopping = false
    var stopRequested = false
    var finished = false

    init(directory: String, operation: String, seconds: Int) throws {
        self.directory = directory
        self.operation = operation
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
        configuration.entropyDevices = [VZVirtioEntropyDeviceConfiguration()]
        // No NAT, host network, sockets, shared folders, clipboard or guest agent.
        configuration.networkDevices = []
        configuration.directorySharingDevices = []
        configuration.socketDevices = []
        configuration.serialPorts = []
        try configuration.validate()
        vm = VZVirtualMachine(configuration: configuration)
        super.init()
        vm.delegate = self
    }

    func start() throws {
        try record(directory, "attempt.json", ["operation": operation, "deadline": deadline.timeIntervalSince1970])
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
                    try record(directory, "running.json", ["operation": operation, "observed_at": Date().timeIntervalSince1970, "network_devices": 0])
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
    guard CommandLine.arguments.count == 4 else { throw fail("usage: node-vm-macos PRIVATE_OPERATION_DIRECTORY EXECUTION_ID SECONDS") }
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
    let launcher = try Launcher(directory: directory, operation: operation, seconds: seconds)
    try launcher.start()
    withExtendedLifetime(launcher) { RunLoop.main.run() }
} catch {
    terminate((error as? LauncherError)?.message ?? "VM configuration unavailable")
}
