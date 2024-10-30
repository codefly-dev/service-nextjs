import { getServerSession } from "next-auth/next"
import { redirect } from "next/navigation"
import Image from "next/image"
import { profileStyles } from "./styles"

export default async function ProfilePage() {
  const session = await getServerSession()

  if (!session?.user) {
    redirect('/api/auth/signin')
  }

  return (
    <div className={profileStyles.container}>
      <div className={profileStyles.card}>
        <div className={profileStyles.header}></div>
        <div className={profileStyles.content}>
          <div className={profileStyles.avatarWrapper}>
            <Image
              className={profileStyles.avatar}
              src={session.user.image || `https://ui-avatars.com/api/?name=${encodeURIComponent(session.user.name || 'User')}&background=random`}
              alt={session.user.name || "User avatar"}
              width={192}
              height={192}
            />
          </div>
          <div className={profileStyles.nameWrapper}>
            <h1 className={profileStyles.name}>
              {session.user.name}
            </h1>
            <p className={profileStyles.email}>
              {session.user.email}
            </p>
          </div>
          <div className={profileStyles.detailsWrapper}>
            <dl className={profileStyles.detailsList}>
              <div className={profileStyles.detailItem}>
                <dt className={profileStyles.detailLabel}>Full name</dt>
                <dd className={profileStyles.detailValue}>{session.user.name}</dd>
              </div>
              <div className={profileStyles.detailItem}>
                <dt className={profileStyles.detailLabel}>Email address</dt>
                <dd className={profileStyles.detailValue}>{session.user.email}</dd>
              </div>
              {/* Add more user details here as needed */}
            </dl>
          </div>
          <div className={profileStyles.buttonWrapper}>
            <button className={profileStyles.button}>
              Edit Profile
            </button>
          </div>
        </div>
      </div>
    </div>
  )
}
