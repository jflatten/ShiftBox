on run argv
    set sourceBundle to POSIX file (item 1 of argv) as alias
    set finalBundle to POSIX file (item 2 of argv)
    tell application "/Applications/UTM.app"
        if UTM version is not "5.0.8" then error "Unsupported UTM version"
        set vm to import new virtual machine from sourceBundle
        try
            if status of vm is not stopped or backend of vm is not qemu then error "Unexpected export VM state"
            set vmID to id of vm
            set cfg to configuration of vm
            set buildNetworks to network interfaces of cfg
            if (count of buildNetworks) is not 2 then error "Unexpected build network layout"
            if mode of item 1 of buildNetworks is not shared then error "Shared network is missing"
            if mode of item 2 of buildNetworks is not emulated then error "Build network is missing"
            set buildDrives to drives of cfg
            if (count of buildDrives) is not 1 then error "Unexpected build drive layout"
            set networkIndex to index of item 1 of buildNetworks
            set diskID to id of item 1 of buildDrives
            set finishedNetworks to {{index:networkIndex}}
            set preservedDrives to {{id:diskID}, {removable:true}}
            update configuration vm with {network interfaces:finishedNetworks, drives:preservedDrives}
            set vm to virtual machine id vmID
            set finishedConfig to configuration of vm
            set verifiedNetworks to network interfaces of finishedConfig
            if (count of verifiedNetworks) is not 1 then error "Build network remained attached"
            set finishedDrives to drives of finishedConfig
            if (count of finishedDrives) is not 2 then error "Recovery drive is missing"
            if not (removable of item 2 of finishedDrives) then error "Recovery drive is not removable"
            if id of item 1 of finishedDrives is not id of item 1 of buildDrives then error "Persistent drive changed"
            export vm to finalBundle
        on error messageText number errorNumber
            if status of vm is stopped then delete vm
            error messageText number errorNumber
        end try
        delete vm
    end tell
end run
