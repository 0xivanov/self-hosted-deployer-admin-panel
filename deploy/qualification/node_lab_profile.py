"""Shared service settings for the trusted Linux qualification fixtures."""
import os


def restriction_properties():
    return {
        'User': str(os.getuid()), 'Group': str(os.getgid()),
        'NoNewPrivileges': 'yes', 'PrivateNetwork': 'yes', 'PrivateDevices': 'yes',
        'ProtectSystem': 'strict', 'ProtectHome': 'yes', 'ProtectControlGroups': 'yes',
        'ProtectKernelTunables': 'yes', 'ProtectKernelModules': 'yes',
        'RestrictSUIDSGID': 'yes', 'RestrictNamespaces': 'yes', 'CapabilityBoundingSet': '',
        'MemoryMax': '128M', 'MemorySwapMax': '0', 'TasksMax': '32', 'CPUQuota': '100%',
        'RuntimeMaxSec': '10', 'TimeoutStopSec': '2', 'KillMode': 'control-group',
        'LimitFSIZE': '4M',
        'TemporaryFileSystem': f'/work:rw,size=64M,mode=0700,uid={os.getuid()},gid={os.getgid()} /tmp:rw,size=16M,mode=1777 /var/tmp:rw,size=16M,mode=1777',
        'WorkingDirectory': '/work',
    }
