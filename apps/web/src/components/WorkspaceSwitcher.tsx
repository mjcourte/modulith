import { DropdownMenu, DropdownMenuContent, DropdownMenuRadioGroup, DropdownMenuRadioItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { SidebarMenuButton } from "@/components/ui/sidebar";
import { useWorkspace, type WorkspaceKind } from "@/lib/workspace-context";
import { BuildingIcon, CaretUpDownIcon, UserIcon } from "@phosphor-icons/react";

export function WorkspaceSwitcher() {
    const { kind, setKind, org, user } = useWorkspace()

    if (!org) {
        return (
            <SidebarMenuButton size="lg">
                <UserIcon />
                <span className="grid flex-1 text-left leading-tight">
                    <span className="text-xs text-muted-foreground">Acting as</span>
                    <span className="truncate font-medium">{user.name}</span>
                </span>
            </SidebarMenuButton>
        )
    }

    const Icon = kind === 'org' ? BuildingIcon : UserIcon

    return (
        <DropdownMenu>
            <DropdownMenuTrigger render={<SidebarMenuButton size="lg" />}>
                <Icon />
                <span className="grid flex-1 text-left leading-tight">
                    <span className="text-xs text-muted-foreground">Acting as</span>
                    <span className="truncate font-medium">{kind === 'org' ? org.name : user.name}</span>
                </span>
                <CaretUpDownIcon className="ml-auto" />
            </DropdownMenuTrigger>
            <DropdownMenuContent>
                <DropdownMenuRadioGroup value={kind} onValueChange={v => setKind(v as WorkspaceKind)}>
                    <DropdownMenuRadioItem value='user'>
                        <UserIcon />
                        {user.name}
                    </DropdownMenuRadioItem>
                    <DropdownMenuRadioItem value='org'>
                        <BuildingIcon />
                        {org.name}
                    </DropdownMenuRadioItem>
                </DropdownMenuRadioGroup>
            </DropdownMenuContent>
        </DropdownMenu>
    )
}