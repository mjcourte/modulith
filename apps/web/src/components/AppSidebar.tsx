import { Sidebar, SidebarContent, SidebarFooter, SidebarGroup, SidebarHeader, SidebarMenu, SidebarMenuButton, SidebarMenuItem } from "@/components/ui/sidebar";
import { UserNav } from "@/components/UserNav";
import { WorkspaceSwitcher } from "@/components/WorkspaceSwitcher";
import { BuildingIcon, FolderIcon, HouseIcon, ImagesIcon, ShareIcon, TrashIcon } from "@phosphor-icons/react";
import { Link, useMatchRoute } from "@tanstack/react-router";

// fuzzy keeps an item lit on the pages below it; Home must match exactly,
// as a fuzzy '/' matches every page.
const nav = [
    { to: '/', label: 'Home', icon: HouseIcon, fuzzy: false },
    { to: '/projects', label: 'Projects', icon: FolderIcon, fuzzy: true },
    { to: '/library', label: 'Asset Library', icon: ImagesIcon, fuzzy: true },
    { to: '/shared', label: 'Shared', icon: ShareIcon, fuzzy: true },
    { to: '/org', label: 'Organization', icon: BuildingIcon, fuzzy: true },
    { to: '/trash', label: 'Trash', icon: TrashIcon, fuzzy: true },
] as const

export function AppSidebar() {
    const matchRoute = useMatchRoute()

    return (
        <Sidebar>
            <SidebarHeader>
                <SidebarMenu>
                    <SidebarMenuItem>
                        <WorkspaceSwitcher />
                    </SidebarMenuItem>
                </SidebarMenu>
            </SidebarHeader>
            <SidebarContent>
                <SidebarGroup>
                    <SidebarMenu>
                        {nav.map(item => (
                            <SidebarMenuItem key={item.to}>
                                <SidebarMenuButton render={<Link to={item.to} />} isActive={!!matchRoute({ to: item.to, fuzzy: item.fuzzy })}
                                    // the theme's active colour is barely distinct from the sidebar's
                                    className="data-active:bg-primary data-active:text-primary-foreground">
                                    <item.icon />
                                    <span>{item.label}</span>
                                </SidebarMenuButton>
                            </SidebarMenuItem>
                        ))}
                    </SidebarMenu>
                </SidebarGroup>
            </SidebarContent>
            <SidebarFooter>
                <UserNav />
            </SidebarFooter>
        </Sidebar>
    )
}
