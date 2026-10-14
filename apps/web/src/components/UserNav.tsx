import { SidebarMenu, SidebarMenuButton, SidebarMenuItem } from "@/components/ui/sidebar";
import { UserAvatar } from "@/components/UserAvatar";
import { useWorkspace } from "@/lib/workspace-context";
import { GearIcon, SignOutIcon } from "@phosphor-icons/react";
import { Link, useMatchRoute } from "@tanstack/react-router";
import Session from "supertokens-auth-react/recipe/session";

// The sidebar's footer: who is signed in, their settings, and sign out.
export function UserNav() {
    const { user, org } = useWorkspace()
    const matchRoute = useMatchRoute()

    return (
        <SidebarMenu>
            <SidebarMenuItem>
                {/* display only: a div, not a button */}
                <SidebarMenuButton size="lg" render={<div />} className="hover:bg-transparent">
                    <UserAvatar user={user} />
                    <span className="grid flex-1 text-left leading-tight">
                        <span className="truncate font-medium">{user.name}</span>
                        <span className="truncate text-xs text-muted-foreground">{user.email}</span>
                        {org && <span className="truncate text-xs text-muted-foreground">{org.name}</span>}
                    </span>
                </SidebarMenuButton>
            </SidebarMenuItem>
            <SidebarMenuItem>
                <SidebarMenuButton render={<Link to="/profile" />} isActive={!!matchRoute({ to: '/profile' })}
                    className="data-active:bg-primary data-active:text-primary-foreground">
                    <GearIcon />
                    <span>Settings</span>
                </SidebarMenuButton>
            </SidebarMenuItem>
            <SidebarMenuItem>
                <SidebarMenuButton className="text-muted-foreground" onClick={async () => {
                    await Session.signOut()
                    window.location.assign('/login')
                }}>
                    <SignOutIcon />
                    <span>Sign out</span>
                </SidebarMenuButton>
            </SidebarMenuItem>
        </SidebarMenu>
    )
}
