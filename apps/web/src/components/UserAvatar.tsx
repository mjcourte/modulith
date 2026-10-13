import type { ComponentProps } from 'react'
import type { User } from '@/api/me'
import { Avatar, AvatarFallback, AvatarImage } from '@/components/ui/avatar'

type Props = {
    user: Pick<User, 'name' | 'email' | 'avatarUrl'>
    size?: ComponentProps<typeof Avatar>['size']
}

// The user's provider picture, or their initials while it loads, fails or is
// missing.
export function UserAvatar({ user, size }: Props) {
    return (
        <Avatar size={size}>
            {/* Google's avatar servers reject requests with a localhost Referer. */}
            <AvatarImage src={user.avatarUrl} alt="" referrerPolicy="no-referrer" />
            <AvatarFallback>{initials(user)}</AvatarFallback>
        </Avatar>
    )
}

function initials({ name, email }: Pick<User, 'name' | 'email'>): string {
    const fromName = name.trim().split(/\s+/).map(word => word[0]).slice(0, 2).join('')
    return (fromName || email[0] || '?').toUpperCase()
}
